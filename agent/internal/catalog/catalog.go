// Package catalog is the fixed list of images the agent may start. Every image is pinned by digest; the agent
// never runs an image that is not listed here. Updating an image = a new agent release.
package catalog

// PHPVersion is one offered PHP version with its WordPress images.
type PHPVersion struct {
	Version string
	Apache  string // wordpress:<wp>-php<v>-apache
	FPM     string // wordpress:<wp>-php<v>-fpm
	CLI     string // wordpress:cli-php<v>
	// EOL is true when PHP itself gets no more security fixes; the panel shows a warning and the image is frozen.
	EOL bool
	// SecurityUntil is the last day of upstream security fixes (YYYY-MM-DD).
	SecurityUntil string
}

// Default is the PHP version preselected in the panel.
const Default = "8.4"

var php = []PHPVersion{
	{
		Version: "8.1", EOL: true, SecurityUntil: "2025-12-31",
		Apache: "wordpress:6.9.0-php8.1-apache@sha256:f73396626d2f1b89d4c556917c15f55cfcc6b5f80595205516e490f0f04015e4",
		FPM:    "wordpress:6.9.0-php8.1-fpm@sha256:972d6b6914562b352b85e52d347c0d3404c6294fbad064f2b5017623a65b4bd5",
		CLI:    CLI,
	},
	{
		Version: "8.2", SecurityUntil: "2026-12-31",
		Apache: "wordpress:7.1.4-php8.2-apache@sha256:6b4e965780d43e67677af8b88d39611b5ae1ea71650b5ec8b74e63ba4cecbf04",
		FPM:    "wordpress:7.1.4-php8.2-fpm@sha256:2ca4a24b763bcc21d2784b1425bbf9ebc177d092da7a2025eedacfd7745e7a83",
		CLI:    CLI,
	},
	{
		Version: "8.3", SecurityUntil: "2027-12-31",
		Apache: "wordpress:7.1.4-php8.3-apache@sha256:9001ea7dafd2bc3f05d068e6413b0edd2a116eeed4dfe9e08b6bd718dee4960a",
		FPM:    "wordpress:7.1.4-php8.3-fpm@sha256:812a11f25e7cd36000719e109c8730882e6d2b82ab53bb1a9fde309fdd9d5410",
		CLI:    CLI,
	},
	{
		Version: "8.4", SecurityUntil: "2028-12-31",
		Apache: "wordpress:7.1.4-php8.4-apache@sha256:543d2b75f287c76f387cd17dcf2ca5e69745a1c30b754e32414a5e1ef5b8da87",
		FPM:    "wordpress:7.1.4-php8.4-fpm@sha256:8f5afd664b5f35d83b0e52d4f62952456313460ac58678b460c59e74eca1c79c",
		CLI:    CLI,
	},
	{
		Version: "8.5", SecurityUntil: "2029-12-31",
		Apache: "wordpress:7.1.4-php8.5-apache@sha256:1065422531e9f1741e393ebd27e235a92edeb3336142b66d781ff5d8c90e0392",
		FPM:    "wordpress:7.1.4-php8.5-fpm@sha256:b98cac329c82e1b23a0cc01bba63e5d33500ab1efc8c9e01d423266fe24afe85",
		CLI:    CLI,
	},
}

// Shared images.
const (
	CLI     = "wordpress:cli-php8.4@sha256:37b7e3ed9b537788756f9b5c6dd8ec7c3f3053ee707abc67c776e453ba8f9e55"
	MariaDB = "mariadb:11.8@sha256:c4124d1afa875e47f2892edf26b482fa54f796e2f342d3717a04ca091a22a9bd"
	Nginx   = "nginx:1.30-alpine@sha256:583b7f2fce1cdb8da5ad5c39ebd3c33b98b87d493190e548bdee1fa8c619c311"
)

// PHP returns the entry for a version.
func PHP(v string) (PHPVersion, bool) {
	for _, p := range php {
		if p.Version == v {
			return p, true
		}
	}
	return PHPVersion{}, false
}

// All returns the offered versions, oldest first.
func All() []PHPVersion { return append([]PHPVersion(nil), php...) }
