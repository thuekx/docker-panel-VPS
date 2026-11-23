package sites

// Optimize Website (PHP-01), like cPanel's Optimize Website, for every app and both engines: the edge compresses
// text answers (gzip, Brotli or zstd, as the browser asks) and tells browsers to keep static files (styles,
// scripts, images, fonts, media) for a number of days. Both are Traefik middlewares on the website's routers.

import (
	"fmt"
	"slices"
	"strings"
)

// Optimize is what the edge does for a website's answers (nil = nothing).
type Optimize struct {
	Compress  bool `json:"compress,omitempty"`
	CacheDays int  `json:"cache_days,omitempty"` // 0 = the website's own cache headers
}

// CacheDays are the browser cache times offered.
var CacheDays = []int{1, 7, 30, 365}

// CacheExts are the static files that get the browser cache header.
var CacheExts = []string{"css", "js", "mjs", "png", "jpe?g", "gif", "webp", "avif", "svg", "ico", "woff2?", "ttf", "otf", "eot", "mp4", "webm", "mp3", "ogg", "pdf"}

// compressTypes are the answers the edge compresses; images, video and archives are compressed already.
var compressTypes = []string{"text/html", "text/css", "text/plain", "text/xml", "text/javascript", "application/javascript",
	"application/json", "application/xml", "application/rss+xml", "application/atom+xml", "application/ld+json",
	"application/manifest+json", "image/svg+xml", "application/wasm", "font/ttf", "font/otf"}

// prioCache sits below the security routers: a blocked, hotlinked or protected file never gets the cache header.
const prioCache = 5000

func (s Site) compressMW() string { return "site-" + s.ID + "-compress" }
func (s Site) cacheMW() string    { return "site-" + s.ID + "-cache" }

// optimizeMiddlewares come first on every router that serves the website.
func (s Site) optimizeMiddlewares() []string {
	if s.Optimize != nil && s.Optimize.Compress {
		return []string{s.compressMW()}
	}
	return nil
}

// chainWith is the middleware chain of a router that serves the website: compression, the router's own
// middlewares, the website's headers for all answers (SITE-03), then the redirects. Of two header middlewares
// setting the same header, the one listed first wins, so a folder's headers and the browser cache come before the
// website-wide headers.
func (s Site) chainWith(own ...string) []string {
	out := append(s.optimizeMiddlewares(), own...)
	if h := s.siteHeadersMW(); h != "" {
		out = append(out, h)
	}
	return append(out, s.redirectMiddlewares()...)
}

// servedMiddlewares is the chain of the canonical router: compression, then the redirects.
func (s Site) servedMiddlewares() []string {
	return s.chainWith()
}

// nextOptimize applies an optimize-set change.
func nextOptimize(s Site, next *Site, c SecurityChange) (bool, error) {
	if c.Op != "optimize-set" {
		return false, nil
	}
	if c.Days != 0 && !slices.Contains(CacheDays, c.Days) {
		return true, fmt.Errorf("%w: browser cache of %d days is not offered", ErrBadInput, c.Days)
	}
	o := &Optimize{Compress: c.Compress, CacheDays: c.Days}
	if !o.Compress && o.CacheDays == 0 {
		o = nil
	}
	if (o == nil && s.Optimize == nil) || (o != nil && s.Optimize != nil && *o == *s.Optimize) {
		return true, ErrNoChange
	}
	next.Optimize = o
	return true, nil
}

// optimizeEdge adds the cache router and the optimize middlewares to the website's edge entries.
func (s Site) optimizeEdge(canon, dollar string) ([]edgeRouter, []edgeMiddleware) {
	if s.Optimize == nil {
		return nil, nil
	}
	var rs []edgeRouter
	var ms []edgeMiddleware
	if s.Optimize.Compress {
		ms = append(ms, edgeMiddleware{name: s.compressMW(), kind: "compress", value: strings.Join(compressTypes, ",")})
	}
	if s.Optimize.CacheDays > 0 {
		ms = append(ms, edgeMiddleware{name: s.cacheMW(), kind: "cache", value: fmt.Sprintf("public, max-age=%d", s.Optimize.CacheDays*86400)})
		rs = append(rs, edgeRouter{name: "site-" + s.ID + "-static", priority: prioCache,
			rule:        hostRule([]string{canon}) + " && PathRegexp(`(?i)[.](" + strings.Join(CacheExts, "|") + ")" + dollar + "`)",
			middlewares: s.chainWith(s.cacheMW())})
	}
	return rs, ms
}
