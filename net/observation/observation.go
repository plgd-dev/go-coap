package observation

import "time"

// ObservationSequenceTimeout defines how long is sequence number is valid. https://tools.ietf.org/html/rfc7641#section-3.4
const ObservationSequenceTimeout = 128 * time.Second

// ValidSequenceNumber implements conditions in https://tools.ietf.org/html/rfc7641#section-3.4
func ValidSequenceNumber(old, new uint32, lastEventOccurs time.Time, now time.Time) bool {
	if (old < new && new-old < 2^23) ||
		(old > new && old-new > 2^23) ||
		(now.Sub(lastEventOccurs) > ObservationSequenceTimeout) {
		return true
	}
	return false
}
