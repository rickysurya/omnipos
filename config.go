package main

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Config is read once at startup. Anything missing or weak stops the process before it listens.
type Config struct {
	Port              string
	DatabaseURL       string
	BotToken          string
	TelegramSecret    string
	KDSToken          string
	KDSAllowedOrigins []string
	Currency          string // ISO 4217 code shown next to prices
	CurrencyDecimals  int    // minor unit digits; 0 means price_cents holds whole units
}

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

func loadConfig(getenv func(string) string) (Config, error) {
	var errs []error
	required := func(name string) string {
		value := strings.TrimSpace(getenv(name))
		if value == "" {
			errs = append(errs, fmt.Errorf("%s is not set", name))
		}
		return value
	}

	cfg := Config{
		DatabaseURL:    required("DATABASE_URL"),
		BotToken:       required("TELEGRAM_BOT_TOKEN"),
		TelegramSecret: required("TELEGRAM_SECRET_TOKEN"),
		KDSToken:       required("KDS_TOKEN"),
		Port:           strings.TrimSpace(getenv("PORT")),
	}

	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if n, err := strconv.Atoi(cfg.Port); err != nil || n < 1 || n > 65535 {
		errs = append(errs, errors.New("PORT must be a number between 1 and 65535"))
	}

	if cfg.TelegramSecret != "" {
		if err := validateSecret("TELEGRAM_SECRET_TOKEN", cfg.TelegramSecret); err != nil {
			errs = append(errs, err)
		}
	}

	if cfg.KDSToken != "" {
		if err := validateSecret("KDS_TOKEN", cfg.KDSToken); err != nil {
			errs = append(errs, err)
		}
	}

	if cfg.KDSToken != "" && cfg.KDSToken == cfg.TelegramSecret {
		errs = append(errs, errors.New("KDS_TOKEN must differ from TELEGRAM_SECRET_TOKEN"))
	}

	origins, err := parseOrigins(getenv("KDS_ALLOWED_ORIGINS"))
	if err != nil {
		errs = append(errs, err)
	}
	cfg.KDSAllowedOrigins = origins

	cfg.Currency = strings.ToUpper(strings.TrimSpace(getenv("CURRENCY")))
	if cfg.Currency == "" {
		cfg.Currency = "IDR"
	}
	if !currencyPattern.MatchString(cfg.Currency) {
		errs = append(errs, errors.New("CURRENCY must be a three letter ISO 4217 code such as IDR"))
	}
	if raw := strings.TrimSpace(getenv("CURRENCY_DECIMALS")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 3 {
			errs = append(errs, errors.New("CURRENCY_DECIMALS must be a number from 0 to 3"))
		}
		cfg.CurrencyDecimals = n
	}

	return cfg, errors.Join(errs...)
}

// parseOrigins accepts a comma separated list of browser origins such as https://kds.example.com.
// Entries are normalized to lower case so they compare equal to what browsers send.
func parseOrigins(raw string) ([]string, error) {
	var origins []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		u, err := url.Parse(part)
		invalid := err != nil ||
			(u.Scheme != "http" && u.Scheme != "https") ||
			u.Host == "" ||
			u.User != nil || u.ForceQuery || strings.Contains(part, "#") ||
			(u.Path != "" && u.Path != "/") ||
			u.RawQuery != "" || u.Fragment != ""
		if invalid {
			return nil, fmt.Errorf("KDS_ALLOWED_ORIGINS entry %q must look like https://host[:port]", part)
		}
		host := strings.ToLower(u.Host)
		if (u.Scheme == "https" && strings.HasSuffix(host, ":443")) ||
			(u.Scheme == "http" && strings.HasSuffix(host, ":80")) {
			host = host[:strings.LastIndex(host, ":")]
		}
		origins = append(origins, strings.ToLower(u.Scheme+"://"+u.Host))
	}
	return origins, nil
}
