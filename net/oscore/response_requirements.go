package oscore

import "time"

// ResponseRequirements tracks tokens and exchanges that require OSCORE
// protection on their responses. Callers serialize access to this type.
type ResponseRequirements map[string]map[Exchange]time.Time

// NewResponseRequirements creates an empty response requirement tracker.
func NewResponseRequirements() ResponseRequirements {
	return make(ResponseRequirements)
}

// Add records an exchange requirement. A zero expiry means no time-based
// expiry while the transport consumer remains active.
func (r ResponseRequirements) Add(token string, exchange Exchange, expires time.Time) {
	if r[token] == nil {
		r[token] = make(map[Exchange]time.Time)
	}
	r[token][exchange] = expires
}

// Has reports whether token has an unexpired response requirement.
func (r ResponseRequirements) Has(token string, now time.Time) bool {
	requirements := r[token]
	for exchange, expires := range requirements {
		if !expires.IsZero() && !now.Before(expires) {
			delete(requirements, exchange)
		}
	}
	if len(requirements) == 0 {
		delete(r, token)
		return false
	}
	return true
}

// HasToken reports whether token has any response requirement, regardless of expiry.
func (r ResponseRequirements) HasToken(token string) bool {
	_, exists := r[token]
	return exists
}

// Retain keeps an exchange requirement active until explicitly removed.
func (r ResponseRequirements) Retain(token string, exchange Exchange) {
	if requirements := r[token]; requirements != nil {
		if _, exists := requirements[exchange]; exists {
			requirements[exchange] = time.Time{}
		}
	}
}

// Remove removes one exchange requirement.
func (r ResponseRequirements) Remove(token string, exchange Exchange) {
	requirements := r[token]
	delete(requirements, exchange)
	if len(requirements) == 0 {
		delete(r, token)
	}
}

// RemoveMatching removes requirements matching predicate.
func (r ResponseRequirements) RemoveMatching(token string, predicate func(Exchange) bool) {
	if predicate == nil {
		return
	}
	for exchange := range r[token] {
		if predicate(exchange) {
			r.Remove(token, exchange)
		}
	}
}

// RemoveToken removes every requirement for token.
func (r ResponseRequirements) RemoveToken(token string) {
	delete(r, token)
}

// Expire removes all requirements whose non-zero expiry has passed.
func (r ResponseRequirements) Expire(now time.Time) {
	for token := range r {
		r.Has(token, now)
	}
}
