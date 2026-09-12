package digitalocean

import "testing"

// normalizeAuthorizedKey decides whether the SSH key registered with
// DigitalOcean is the same key whose private half we hold locally. Getting this
// wrong is not cosmetic: `adhar cluster delete` removes the cluster state
// directory that holds the private key, so the next `adhar up` generates a new
// keypair while a key of the same NAME is still registered. Matching on name
// alone reused that stale id and built a cluster nobody could SSH into.
func TestNormalizeAuthorizedKey(t *testing.T) {
	const body = "AAAAC3NzaC1lZDI1NTE5AAAAIB2vLb3qz3Uq0JmH5f5kZ8m3nq0d5r5tO1kq0mQ8xYzA"

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "type and body only",
			in:   "ssh-ed25519 " + body,
			want: "ssh-ed25519 " + body,
		},
		{
			name: "trailing comment is ignored",
			in:   "ssh-ed25519 " + body + " adhar cluster dev",
			want: "ssh-ed25519 " + body,
		},
		{
			name: "surrounding whitespace and newline are ignored",
			in:   "  ssh-ed25519 " + body + " someone@host\n",
			want: "ssh-ed25519 " + body,
		},
		{
			name: "extra internal spacing is collapsed",
			in:   "ssh-ed25519   " + body + "   comment",
			want: "ssh-ed25519 " + body,
		},
		{
			name: "malformed single field is returned trimmed",
			in:   "  garbage\n",
			want: "garbage",
		},
		{
			name: "empty stays empty",
			in:   "   ",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeAuthorizedKey(tc.in); got != tc.want {
				t.Fatalf("normalizeAuthorizedKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Two keys differing only by comment must compare equal (the same key, so it is
// reused), and two genuinely different keys must not (the stale one is replaced).
func TestNormalizeAuthorizedKeyComparison(t *testing.T) {
	const bodyA = "AAAAC3NzaC1lZDI1NTE5AAAAIB2vLb3qz3Uq0JmH5f5kZ8m3nq0d5r5tO1kq0mQ8xYzA"
	const bodyB = "AAAAC3NzaC1lZDI1NTE5AAAAICzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"

	local := "ssh-ed25519 " + bodyA + " adhar cluster dev\n"
	sameRegistered := "ssh-ed25519 " + bodyA + " registered-by-someone-else"
	otherRegistered := "ssh-ed25519 " + bodyB + " adhar cluster dev"

	if normalizeAuthorizedKey(local) != normalizeAuthorizedKey(sameRegistered) {
		t.Fatal("the same key with a different comment must be treated as a match and reused")
	}
	if normalizeAuthorizedKey(local) == normalizeAuthorizedKey(otherRegistered) {
		t.Fatal("a different key must NOT be treated as a match; it has to be replaced")
	}
}
