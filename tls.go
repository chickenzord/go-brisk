package brisk

import (
	"crypto/rand"
	"math/big"

	utls "github.com/refraction-networking/utls"
)

// TLSProfile encapsulates a TLS fingerprint specification without leaking third-party types.
type TLSProfile struct {
	id utls.ClientHelloID
}

// ClientHelloID returns the underlying uTLS ClientHelloID.
func (p TLSProfile) ClientHelloID() utls.ClientHelloID {
	return p.id
}

// String returns a human-readable identifier for the profile.
func (p TLSProfile) String() string {
	return p.id.Client
}

// Predefined modern browser TLS fingerprint profiles.
var (
	TLSProfileChrome120  = TLSProfile{id: utls.HelloChrome_120}
	TLSProfileChrome102  = TLSProfile{id: utls.HelloChrome_102}
	TLSProfileFirefox120 = TLSProfile{id: utls.HelloFirefox_120}
	TLSProfileFirefox105 = TLSProfile{id: utls.HelloFirefox_105}
	TLSProfileSafari16   = TLSProfile{id: utls.HelloSafari_16_0}
	TLSProfileEdge106    = TLSProfile{id: utls.HelloEdge_106}
)

// DefaultTLSProfile is the default browser TLS fingerprint (Chrome 120).
var DefaultTLSProfile = TLSProfileChrome120

// Standard browser profiles for rotation.
var standardBrowserProfiles = []TLSProfile{
	TLSProfileChrome120,
	TLSProfileChrome102,
	TLSProfileFirefox120,
	TLSProfileFirefox105,
	TLSProfileSafari16,
	TLSProfileEdge106,
}

// TLSProfileSelector dynamically selects a TLSProfile.
type TLSProfileSelector func() TLSProfile

// RandomBrowserProfile returns a dynamically chosen browser TLS fingerprint profile.
func RandomBrowserProfile() TLSProfile {
	idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(standardBrowserProfiles))))
	if err != nil {
		return DefaultTLSProfile
	}
	return standardBrowserProfiles[idx.Int64()]
}

// NewProfileRotator creates a TLSProfileSelector that chooses uniformly from the provided profiles.
func NewProfileRotator(profiles ...TLSProfile) TLSProfileSelector {
	if len(profiles) == 0 {
		profiles = standardBrowserProfiles
	}
	copied := make([]TLSProfile, len(profiles))
	copy(copied, profiles)

	return func() TLSProfile {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(copied))))
		if err != nil {
			return copied[0]
		}
		return copied[idx.Int64()]
	}
}
