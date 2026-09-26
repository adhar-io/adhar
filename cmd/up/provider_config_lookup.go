package up

import (
	"strconv"
	"strings"
)

// Keys in a provider's `config:` map arrive LOWER-CASED from config decoding, so
// an exact-case lookup for "subscriptionId" or "dnsResourceGroup" never matches
// what is actually in the map. That has now bitten in three separate places:
// the Azure provider read five of seven settings as empty and built a cluster in
// a resource group nobody asked for; the edge DNS step reported all five Azure
// values missing while the file plainly set them; and `azure-credentials` was
// never materialised, which left the node autoscaler unable to add a worker
// ("reading azure-credentials: Secret not found") on a cluster that needed one.
// One helper now, so a fourth copy cannot drift.

// normaliseConfigKey strips case and the separators a key might be spelled with,
// so subscriptionId, subscription_id and SUBSCRIPTION-ID all match.
func normaliseConfigKey(k string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(k) {
		if r == '_' || r == '-' || r == ' ' || r == '.' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// providerConfigString reads one key from a provider's `config:` map, matching
// the key however it is spelled and rendering whatever scalar type the decoder
// produced. Numbers and booleans are NOT strings in a decoded config — a
// string-only type assertion silently drops `diskSizeGb: 256`.
func providerConfigString(cfg map[string]interface{}, key string) string {
	if cfg == nil {
		return ""
	}
	if v, ok := cfg[key]; ok {
		if s := configScalar(v); s != "" {
			return s
		}
	}
	want := normaliseConfigKey(key)
	for k, v := range cfg {
		if normaliseConfigKey(k) != want {
			continue
		}
		if s := configScalar(v); s != "" {
			return s
		}
	}
	return ""
}

// configScalar renders a decoded YAML scalar as a string.
func configScalar(v interface{}) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case int:
		return strconv.Itoa(t)
	case int32:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return ""
	}
}
