package model

type DeadLetterEvent struct {
	ID          int64  `json:"id"`
	Payload     string `json:"payload"`
	Error       string `json:"error"`
	CreatedAtMS int64  `json:"createdAtMs"`
}
