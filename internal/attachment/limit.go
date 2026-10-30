// Package attachment holds the largest file the Hub carries on any channel.
package attachment

// MaxBytes matches the Relay Core attachment limit. A provider can accept less for one media kind; Core applies that limit.
const MaxBytes = 10 << 20

// EnvelopeBytes bounds a request, stored payload or email that carries attachments of MaxBytes as base64.
const EnvelopeBytes = (MaxBytes+2)/3*4 + 2<<20
