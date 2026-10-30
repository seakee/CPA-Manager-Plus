package upgrade

import "fmt"

// diskCheck compares the copy requirement (source bytes plus 20% headroom)
// with the free space of the volume holding the legacy root. A probe that is
// unavailable on this platform is reported, never silently treated as enough.
func diskCheck(root string, sourceBytes int64, options Options) (Check, bool) {
	required := sourceBytes + (sourceBytes+4)/5
	probe := options.AvailableBytes
	if probe == nil {
		probe = availableBytes
	}
	available, err := probe(root)
	if err != nil {
		return Check{
			Name: CheckDisk, Status: StatusSkipped,
			Detail: fmt.Sprintf("requiredBytes=%d availableBytes=unavailable; sourceBytes=%d", required, sourceBytes),
		}, false
	}
	detail := fmt.Sprintf("requiredBytes=%d availableBytes=%d; sourceBytes=%d", required, available, sourceBytes)
	if available < required {
		return Check{Name: CheckDisk, Status: StatusBlocked, Code: CodeDiskInsufficient, Detail: detail}, true
	}
	return Check{Name: CheckDisk, Status: StatusPass, Detail: detail}, false
}
