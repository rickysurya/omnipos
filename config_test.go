package main

import (
	"strings"
	"testing"
)

func envFrom(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func goodEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":          "postgres://omnipos_app:pw@localhost:5432/omnipos",
		"TELEGRAM_BOT_TOKEN":    "123456:bot-token",
		"TELEGRAM_SECRET_TOKEN": strings.Repeat("a", 32),
		"KDS_TOKEN":             strings.Repeat("b", 32),
	}
}

func TestLoadConfigAcceptsAValidEnvironment(t *testing.T) {
	cfg, err := loadConfig(envFrom(goodEnv()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != "8080" {
		t.Fatalf("default port = %q, want 8080", cfg.Port)
	}
	if len(cfg.KDSAllowedOrigins) != 0 {
		t.Fatalf("origins should default to none, got %v", cfg.KDSAllowedOrigins)
	}
}

func TestLoadConfigReportsEverythingThatIsWrong(t *testing.T) {
	_, err := loadConfig(envFrom(map[string]string{}))
	if err == nil {
		t.Fatal("an empty environment must be rejected")
	}
	for _, name := range []string{"DATABASE_URL", "TELEGRAM_BOT_TOKEN", "TELEGRAM_SECRET_TOKEN", "KDS_TOKEN"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error should mention %s: %v", name, err)
		}
	}
}

func TestLoadConfigRejectsWeakOrReusedSecrets(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(map[string]string)
		wantErr string
	}{
		{"short telegram secret", func(e map[string]string) { e["TELEGRAM_SECRET_TOKEN"] = "short" }, "TELEGRAM_SECRET_TOKEN"},
		{"short kds token", func(e map[string]string) { e["KDS_TOKEN"] = "short" }, "KDS_TOKEN"},
		{"characters telegram forbids", func(e map[string]string) { e["TELEGRAM_SECRET_TOKEN"] = strings.Repeat("a", 31) + "!" }, "TELEGRAM_SECRET_TOKEN"},
		{"same value for both", func(e map[string]string) { e["KDS_TOKEN"] = e["TELEGRAM_SECRET_TOKEN"] }, "must differ"},
		{"port not a number", func(e map[string]string) { e["PORT"] = "http" }, "PORT"},
		{"port out of range", func(e map[string]string) { e["PORT"] = "70000" }, "PORT"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := goodEnv()
			c.mutate(env)
			_, err := loadConfig(envFrom(env))
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, c.wantErr)
			}
		})
	}
}

func TestLoadConfigCurrency(t *testing.T) {
	cfg, err := loadConfig(envFrom(goodEnv()))
	if err != nil || cfg.Currency != "IDR" || cfg.CurrencyDecimals != 0 {
		t.Fatalf("defaults = %q/%d (err %v), want IDR/0", cfg.Currency, cfg.CurrencyDecimals, err)
	}

	env := goodEnv()
	env["CURRENCY"] = " usd "
	env["CURRENCY_DECIMALS"] = "2"
	cfg, err = loadConfig(envFrom(env))
	if err != nil || cfg.Currency != "USD" || cfg.CurrencyDecimals != 2 {
		t.Fatalf("got %q/%d (err %v), want USD/2", cfg.Currency, cfg.CurrencyDecimals, err)
	}

	for name, mutate := range map[string]func(map[string]string){
		"two letter code":     func(e map[string]string) { e["CURRENCY"] = "RP" },
		"digits in code":      func(e map[string]string) { e["CURRENCY"] = "ID1" },
		"decimals not number": func(e map[string]string) { e["CURRENCY_DECIMALS"] = "two" },
		"decimals too many":   func(e map[string]string) { e["CURRENCY_DECIMALS"] = "4" },
		"decimals negative":   func(e map[string]string) { e["CURRENCY_DECIMALS"] = "-1" },
	} {
		env := goodEnv()
		mutate(env)
		if _, err := loadConfig(envFrom(env)); err == nil || !strings.Contains(err.Error(), "CURRENCY") {
			t.Errorf("%s: err = %v, want a CURRENCY error", name, err)
		}
	}
}

func TestParseOrigins(t *testing.T) {
	got, err := parseOrigins(" https://KDS.example.com , http://localhost:3000/ ,, ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"https://kds.example.com", "http://localhost:3000"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("origins = %v, want %v", got, want)
	}

	for _, bad := range []string{"kds.example.com", "ftp://kds.example.com", "https://kds.example.com/path", "https://", "*"} {
		if _, err := parseOrigins(bad); err == nil {
			t.Errorf("parseOrigins(%q) should fail", bad)
		}
	}
}

func TestSecureEqual(t *testing.T) {
	cases := []struct {
		got, want string
		equal     bool
	}{
		{"secret", "secret", true},
		{"secret", "Secret", false},
		{"", "secret", false},
		{"secret", "", false},
		{"", "", false}, // an unset secret never authorizes anything
	}
	for _, c := range cases {
		if secureEqual(c.got, c.want) != c.equal {
			t.Errorf("secureEqual(%q, %q) = %v, want %v", c.got, c.want, !c.equal, c.equal)
		}
	}
}
