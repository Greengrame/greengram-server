// Package config reads the server settings from the environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	DatabaseURL string // GREENGRAM_DATABASE_URL, postgres://...
	DataDir     string // GREENGRAM_DATA_DIR: server.pem, salt.key
	HTTPAddr    string // GREENGRAM_HTTP_ADDR: health, /apiws, later the admin API
	MTProtoAddr string // GREENGRAM_MTPROTO_ADDR: TCP for the apps

	// What clients are told to dial (help.getConfig), and the site URL.
	PublicHost string // GREENGRAM_PUBLIC_HOST
	PublicPort int    // GREENGRAM_PUBLIC_PORT, defaults to the MTProto port
	BaseURL    string // GREENGRAM_BASE_URL

	// GREENGRAM_SECRET_KEY_FILE: phone encryption + lookups. Must be the web
	// server's instance-secret.key when importing its database.
	SecretKeyFile string
	// GREENGRAM_COOKIE_SECURE=1 when the site is served over HTTPS
	SecureCookies bool

	DC         int // GREENGRAM_DC
	ExchangeDC int // GREENGRAM_EXCHANGE_DC, 10000+DC for test-backend clients

	// GREENGRAM_SITE_NAME: what the announcements account is called
	SiteName string
	// GREENGRAM_MINIAPPS_DIR: the mini apps bundle, the captured apps
	// (botfather, stickers, walt, fragment) and greengram/; see cmd/ggminiapps
	MiniAppsDir string
	// Static builds served over HTTP, see scripts/build-web.sh. Empty: not served.
	WebDir   string // GREENGRAM_WEB_DIR: Telegram Web A, at /
	AdminDir string // GREENGRAM_ADMIN_DIR: the admin panel, at /admin
	// GREENGRAM_LANGPACK_DIR, default <data dir>/langpack: language packs,
	// see docs/languages.md. Without any the apps stay on their built-in English.
	LangpackDir string
	// GREENGRAM_ADMIN_CONTACT: how to reach the people running this
	// instance (an e-mail, a URL or a handle), shown on the home page
	AdminContact string
}

func FromEnv() (Config, error) {
	c := Config{
		DatabaseURL: os.Getenv("GREENGRAM_DATABASE_URL"),
		DataDir:     env("GREENGRAM_DATA_DIR", "data"),
		HTTPAddr:    env("GREENGRAM_HTTP_ADDR", "127.0.0.1:8092"),
		MTProtoAddr: env("GREENGRAM_MTPROTO_ADDR", ":44380"),
		PublicHost:  env("GREENGRAM_PUBLIC_HOST", "127.0.0.1"),
		BaseURL:     env("GREENGRAM_BASE_URL", "http://127.0.0.1:8091"),
	}
	c.SecretKeyFile = env("GREENGRAM_SECRET_KEY_FILE", c.DataDir+"/instance-secret.key")
	c.SecureCookies = os.Getenv("GREENGRAM_COOKIE_SECURE") == "1"
	c.SiteName = env("GREENGRAM_SITE_NAME", "Greengram")
	c.MiniAppsDir = env("GREENGRAM_MINIAPPS_DIR", c.DataDir+"/miniapps")
	c.WebDir, c.AdminDir = os.Getenv("GREENGRAM_WEB_DIR"), os.Getenv("GREENGRAM_ADMIN_DIR")
	c.LangpackDir = env("GREENGRAM_LANGPACK_DIR", c.DataDir+"/langpack")
	c.AdminContact = os.Getenv("GREENGRAM_ADMIN_CONTACT")
	if c.DatabaseURL == "" {
		return c, errors.New("GREENGRAM_DATABASE_URL is not set")
	}
	var err error
	if c.DC, err = envInt("GREENGRAM_DC", 2); err != nil {
		return c, err
	}
	// the Android build runs on the test backend and says 10000+dc
	if c.ExchangeDC, err = envInt("GREENGRAM_EXCHANGE_DC", 10000+c.DC); err != nil {
		return c, err
	}
	port := 44380
	if _, p, err := splitPort(c.MTProtoAddr); err == nil {
		port = p
	}
	if c.PublicPort, err = envInt("GREENGRAM_PUBLIC_PORT", port); err != nil {
		return c, err
	}
	return c, nil
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return n, nil
}

func splitPort(addr string) (string, int, error) {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			p, err := strconv.Atoi(addr[i+1:])
			return addr[:i], p, err
		}
	}
	return "", 0, errors.New("no port")
}
