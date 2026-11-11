//go:build windows

package desktopmonitor

import (
	"time"
	"unsafe"
)

// The window paints its own surface, dividers, icon and every label so the
// result matches the approved dark mockup instead of themed dialog controls.
// Native controls still exist for accessibility and hit-testing; they are
// transparent (SS_NOPAINT / WS_EX_TRANSPARENT) and carry the same text.

const (
	colText   = 0xEAF5F5 // #F5F5EA primary text
	colMuted  = 0xA8A29B // #9BA2A8 secondary text
	colAccent = 0xE8B03B // #3BB0E8 blue accent
	colGreen  = 0xA8E05F // #5FE0A8 success
	colWarn   = 0x5FB0F0 // #F0B05F warning
)

func (w *nativeWindow) paint(dc uintptr) {
	var r winRect
	getClientRect.Call(w.hwnd, uintptr(unsafe.Pointer(&r)))
	fillRect.Call(dc, uintptr(unsafe.Pointer(&r)), w.brush)
	setBkMode.Call(dc, 1) // TRANSPARENT
	switch {
	case w.editing:
		w.paintForm(dc)
	case w.prefs.Compact:
		w.paintCompact(dc)
	default:
		w.paintExpanded(dc)
	}
}

// text draws one label with an explicit font and colour at mockup coordinates.
func (w *nativeWindow) text(dc uintptr, font uintptr, colour uintptr, s string, x, y, width, height int, flags uint32) {
	if s == "" || font == 0 {
		return
	}
	selectObject.Call(dc, font)
	setTextColor.Call(dc, colour)
	box := winRect{int32(w.scale(x)), int32(w.scale(y)), int32(w.scale(x + width)), int32(w.scale(y + height))}
	drawText.Call(dc, uintptr(unsafe.Pointer(utf(s))), ^uintptr(0), uintptr(unsafe.Pointer(&box)), uintptr(flags))
}

func (w *nativeWindow) rule(dc uintptr, y int) {
	line := winRect{int32(w.scale(20)), int32(w.scale(y)), int32(w.scale(w.width() - 20)), int32(w.scale(y) + 1)}
	fillRect.Call(dc, uintptr(unsafe.Pointer(&line)), w.softBrush)
}

// icon draws the brand waveform at x,y in the given colour.
func (w *nativeWindow) icon(dc uintptr, x, y int, colour uintptr) {
	pen, _, _ := createPen.Call(0, uintptr(w.scale(2)), colour) // PS_SOLID
	oldPen, _, _ := selectObject.Call(dc, pen)
	size := float64(w.scale(16))
	pts := [][2]float64{{0, .5}, {.22, .5}, {.36, .15}, {.58, .85}, {.74, .5}, {1, .5}}
	firstX, firstY := float64(w.scale(x))+pts[0][0]*size, float64(w.scale(y))+pts[0][1]*size
	moveToEx.Call(dc, uintptr(int(firstX)), uintptr(int(firstY)), 0)
	for _, pt := range pts[1:] {
		lineTo.Call(dc, uintptr(int(float64(w.scale(x))+pt[0]*size)), uintptr(int(float64(w.scale(y))+pt[1]*size)))
	}
	selectObject.Call(dc, oldPen)
	deleteObject.Call(pen)
}

// titlebar draws the drag area, brand and the button cluster.
func (w *nativeWindow) titlebar(dc uintptr, compact bool) {
	height := 46
	if compact {
		height = 33
	}
	line := winRect{0, int32(w.scale(height) - 1), int32(w.scale(w.width())), int32(w.scale(height))}
	fillRect.Call(dc, uintptr(unsafe.Pointer(&line)), w.softBrush)
	if compact {
		w.icon(dc, 14, 9, colAccent)
		w.text(dc, w.smallFont, colText, "CPAMP", 36, 8, 60, 18, 0x20|0x01)
		return
	}
	w.icon(dc, 18, 15, colAccent)
	w.text(dc, w.font, colText, "CPAMP", 40, 13, 90, 22, 0x20|0x01)
}

func (w *nativeWindow) statusLine() (string, uintptr) {
	switch w.state.Status {
	case StatusConnected:
		return w.tr("Connected · recorded requests", "已连接 · 已记录请求"), colGreen
	case StatusOffline:
		return w.tr("Offline · last good data", "连接中断 · 保留上次数据"), colWarn
	case StatusUnauthorized:
		return w.tr("Key rejected · reconnect", "密钥无效 · 请重新连接"), colWarn
	default:
		return w.tr("Disconnected", "未连接"), colMuted
	}
}

func (w *nativeWindow) latestRequest() Request {
	if len(w.state.Snapshot.Requests) > 0 {
		return w.state.Snapshot.Requests[0]
	}
	return Request{Model: "—", InputTokens: -1, OutputTokens: -1, CachedTokens: -1, TotalTokens: -1}
}

func (w *nativeWindow) updatedLine() string {
	if w.state.Snapshot.UpdatedAt.IsZero() {
		return w.tr("Not updated", "尚未更新")
	}
	return w.tr("Updated ", "更新于 ") + w.state.Snapshot.UpdatedAt.Local().Format("01-02 15:04:05")
}

func (w *nativeWindow) paintExpanded(dc uintptr) {
	w.titlebar(dc, false)
	snap := w.state.Snapshot
	r := w.latestRequest()

	status, statusColour := w.statusLine()
	if snap.Partial {
		status += w.tr(" · partial history", " · 历史记录不完整")
	}
	w.text(dc, w.labelFont, colMuted, w.tr("Latest recorded model", "最近调用模型"), 20, 60, 240, 18, 0x20|0x01)
	w.text(dc, w.smallFont, statusColour, status, 20, 60, 400, 18, 0x20|0x02)
	w.text(dc, w.modelFont, colText, r.Model, 20, 84, 400, 32, 0x20|0x01|0x2000)

	desc := w.tr("Not loaded · connect to view requests", "尚未加载 · 连接后显示请求")
	if !snap.UpdatedAt.IsZero() {
		desc = w.tr("No recorded request in 30 days", "近 30 天暂无已记录请求")
	}
	if !r.Timestamp.IsZero() {
		outcome := w.tr("Completed", "已完成")
		if r.Failed {
			outcome = w.tr("Failed", "失败")
		}
		stream := w.tr("nonstream / unknown", "非流式 / 未知")
		if r.Stream {
			stream = w.tr("streaming", "流式")
		}
		desc = outcome + " · " + r.Timestamp.Local().Format("01-02 15:04:05") + " · " + stream
	}
	w.text(dc, w.smallFont, colMuted, desc, 20, 121, 400, 20, 0x20|0x01|0x2000)
	w.rule(dc, 166)

	w.text(dc, w.labelFont, colMuted, w.tr("Output / total latency", "输出 / 总耗时"), 20, 177, 132, 16, 0x20|0x01)
	w.text(dc, w.labelFont, colMuted, w.tr("TTFT (stream)", "首字延迟（流式）"), 158, 177, 125, 16, 0x20|0x01)
	w.text(dc, w.labelFont, colMuted, w.tr("Duration", "总耗时"), 296, 177, 124, 16, 0x20|0x01)
	w.text(dc, w.metricFont, colText, FormatSpeed(r.TokensPerSecond), 20, 197, 132, 30, 0x20|0x01)
	w.text(dc, w.metricFont, colText, FormatDuration(r.TTFTMS), 158, 197, 125, 30, 0x20|0x01)
	w.text(dc, w.metricFont, colText, FormatDuration(r.LatencyMS), 296, 197, 124, 30, 0x20|0x01)
	w.rule(dc, 238)

	w.text(dc, w.labelFont, colMuted, w.tr("REQUEST TOKENS", "本次消耗"), 20, 251, 400, 18, 0x20|0x01)
	tok := func(x int, label string, value int64) {
		w.text(dc, w.labelFont, colMuted, label, x, 275, 120, 16, 0x20|0x01)
		w.text(dc, w.valueFont, colText, FormatNumber(value), x, 293, 120, 24, 0x20|0x01)
	}
	tok(20, w.tr("Input", "输入"), r.InputTokens)
	tok(158, w.tr("Output", "输出"), r.OutputTokens)
	tok(296, w.tr("Total", "合计"), r.TotalTokens)
	w.text(dc, w.smallFont, colMuted, w.tr("Cache hit ", "缓存命中 ")+FormatNumber(r.CachedTokens)+w.tr(" (included in input)", "（已含在输入中）"), 20, 320, 400, 16, 0x20|0x01)
	w.text(dc, w.smallFont, colMuted, w.tr("Request estimate  ", "本次预估费用  ")+FormatCost(r.Cost), 20, 341, 400, 20, 0x20|0x01)
	w.rule(dc, 368)

	w.text(dc, w.labelFont, colMuted, w.tr("TODAY · LOCAL TIME   ", "今日累计 · 本地时间   ")+time.Now().Format("01-02"), 20, 380, 400, 18, 0x20|0x01)
	tok(20, "Token", snap.Today.Tokens)
	tok(158, w.tr("Calls", "请求"), snap.Today.Calls)
	w.text(dc, w.labelFont, colMuted, w.tr("Est. cost", "预估费用"), 296, 403, 120, 16, 0x20|0x01)
	w.text(dc, w.valueFont, colText, FormatCost(snap.Today.Cost), 296, 421, 120, 24, 0x20|0x01)
	w.rule(dc, 456)

	w.text(dc, w.labelFont, colMuted, w.tr("EARLIER REQUESTS", "更早的调用"), 20, 468, 400, 18, 0x20|0x01)
	for i, y := range []int{490, 540} {
		text := w.tr("Earlier request  —", "更早请求  —")
		if len(snap.Requests) > i+1 {
			a := snap.Requests[i+1]
			result := w.tr("completed", "完成")
			if a.Failed {
				result = w.tr("failed", "失败")
			}
			text = a.Model + " · " + result + "\r\n" + a.Timestamp.Local().Format("01-02 15:04:05") + "   ·   " + FormatNumber(a.TotalTokens) + " tokens   ·   " + FormatCost(a.Cost)
		}
		w.text(dc, w.smallFont, colMuted, text, 20, y, 400, 46, 0x20|0x01|0x2000)
	}
	// Footer band like the mockup.
	footer := winRect{0, int32(w.scale(607)), int32(w.scale(w.width())), int32(w.scale(w.height()))}
	fillRect.Call(dc, uintptr(unsafe.Pointer(&footer)), w.softBrush)
	w.text(dc, w.smallFont, colMuted, w.updatedLine(), 20, 620, 195, 22, 0x20|0x01)
	w.text(dc, w.smallFont, colAccent, w.tr("Open panel", "打开面板"), 222, 622, 94, 20, 0x20|0x02)
	w.text(dc, w.smallFont, colMuted, w.tr("Connection", "连接设置"), 322, 622, 98, 20, 0x20|0x02)
}

func (w *nativeWindow) paintCompact(dc uintptr) {
	w.titlebar(dc, true)
	r := w.latestRequest()
	w.text(dc, w.modelFont, colText, r.Model, 14, 38, 200, 22, 0x20|0x01|0x2000)
	w.text(dc, w.smallFont, colMuted, w.tr("Today ", "今日 ")+FormatNumber(w.state.Snapshot.Today.Tokens), 218, 41, 124, 18, 0x20|0x02)
	w.text(dc, w.labelFont, colMuted, w.tr("Output speed", "输出速度"), 14, 70, 104, 16, 0x20|0x01)
	w.text(dc, w.labelFont, colMuted, w.tr("TTFT", "首字"), 122, 70, 104, 16, 0x20|0x01)
	w.text(dc, w.labelFont, colMuted, w.tr("Duration", "耗时"), 230, 70, 104, 16, 0x20|0x01)
	w.text(dc, w.metricFont, colText, FormatSpeed(r.TokensPerSecond), 14, 88, 104, 24, 0x20|0x01)
	w.text(dc, w.metricFont, colText, FormatDuration(r.TTFTMS), 122, 88, 104, 24, 0x20|0x01)
	w.text(dc, w.metricFont, colText, FormatDuration(r.LatencyMS), 230, 88, 104, 24, 0x20|0x01)
	w.text(dc, w.smallFont, colMuted, w.updatedLine(), 14, 118, 328, 16, 0x20|0x01)
}

func (w *nativeWindow) paintForm(dc uintptr) {
	w.titlebar(dc, false)
	w.text(dc, w.smallFont, colMuted, w.tr("Manager URL (not the CPA gateway)", "Manager 地址（不是 CPA 网关）"), 20, 72, 400, 20, 0x20|0x01)
	w.text(dc, w.smallFont, colMuted, w.tr("Administrator key", "管理员密钥"), 20, 150, 400, 20, 0x20|0x01)
	w.text(dc, w.smallFont, colMuted, w.tr("Loopback HTTP or any HTTPS host. The key stays in memory unless you select Remember. No request/account metadata is displayed.", "本机 HTTP 或任意 HTTPS 地址。默认仅在内存保存密钥，勾选后由当前用户 DPAPI 加密。不显示账户或请求正文。"), 20, 296, 400, 58, 0x20|0x01|0x10)
	status := w.tr("Disconnected", "未连接")
	if w.notice != "" {
		status = w.notice
	}
	w.text(dc, w.smallFont, colMuted, status, 20, 400, 400, 22, 0x20|0x01|0x10|0x2000)
}

// drawButton paints one owner-drawn native button: a rounded chip for the
// titlebar cluster, a text link for the panel/connection actions, and a solid
// accent for the primary Connect action.
func (w *nativeWindow) drawButton(item *drawItem) {
	ctrlID, _, _ := getDlgCtrlID.Call(item.Hwnd)
	id := int(ctrlID)
	text := editValue(item.Hwnd, 128)
	hovered := item.State&0x01 != 0
	pressed := item.State&0x04 != 0
	box := item.Rect
	setBkMode.Call(item.DC, 1)
	height := int(box.Bottom - box.Top)

	switch id {
	case IDConnect:
		brush := w.accentBrush
		if pressed {
			brush = w.softBrush
		}
		fillRect.Call(item.DC, uintptr(unsafe.Pointer(&box)), brush)
		selectObject.Call(item.DC, w.font)
		setTextColor.Call(item.DC, 0x211D19)
		drawText.Call(item.DC, uintptr(unsafe.Pointer(utf(text))), ^uintptr(0), uintptr(unsafe.Pointer(&box)), 1|4|0x20)
	case IDOpenPanel, IDConnection:
		if hovered {
			fillRect.Call(item.DC, uintptr(unsafe.Pointer(&box)), w.softBrush)
		}
		selectObject.Call(item.DC, w.smallFont)
		colour := uintptr(colMuted)
		if id == IDOpenPanel {
			colour = colAccent
		}
		setTextColor.Call(item.DC, colour)
		drawText.Call(item.DC, uintptr(unsafe.Pointer(utf(text))), ^uintptr(0), uintptr(unsafe.Pointer(&box)), 1|4|0x20)
	default:
		// Titlebar chips: pin / compact / hide / close.
		colour := uintptr(colMuted)
		switch {
		case id == IDPin && w.prefs.Topmost:
			fillRect.Call(item.DC, uintptr(unsafe.Pointer(&box)), w.softBrush)
			colour = colAccent
		case pressed:
			fillRect.Call(item.DC, uintptr(unsafe.Pointer(&box)), w.softBrush)
			colour = colText
		case hovered:
			fillRect.Call(item.DC, uintptr(unsafe.Pointer(&box)), w.softBrush)
			colour = colText
		}
		font := w.smallFont
		if height < 24 {
			font = w.smallFont
		}
		selectObject.Call(item.DC, font)
		setTextColor.Call(item.DC, colour)
		drawText.Call(item.DC, uintptr(unsafe.Pointer(utf(text))), ^uintptr(0), uintptr(unsafe.Pointer(&box)), 1|4|0x20)
	}
	if item.State&0x10 != 0 {
		drawFocusRect.Call(item.DC, uintptr(unsafe.Pointer(&box)))
	}
}
