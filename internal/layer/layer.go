// Package layer maps the layer a client asks for in invokeWithLayer to one of
// the generated td codec profiles.
package layer

import "github.com/iamxvbaba/td/tlprofile"

// Oldest and Newest bound what we serve. Android (exteraless) is on 231,
// FAgram Desktop and Web A move between 225 and 232.
const (
	Oldest = 225
	Newest = int(tlprofile.ProfileCanonical)
)

// Resolve returns the codec profile for a client layer. Layers newer than
// ours fall back to the newest profile, like Telegram does; older ones are
// refused.
func Resolve(n int) (tlprofile.Profile, bool) {
	if n > Newest {
		return tlprofile.ProfileCanonical, true
	}
	return tlprofile.ResolveProfile(n)
}
