package objectstore

import "testing"

func TestPublicURL(t *testing.T) {
	s := &S3{}
	for _, raw := range []string{"http://cdn.example/fanmade", "https://u:p@cdn.example/fanmade", "https://cdn.example/f?x=1", "https://cdn.example/f#x", "https://cdn.example/f?", "https://cdn.example/a b"} {
		if s.SetPublicBaseURL(raw) == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	if err := s.SetPublicBaseURL("https://cdn.example/fanmade/"); err != nil {
		t.Fatal(err)
	}
	got, err := s.PublicURL("objects/abc/song #?.ogg")
	if err != nil || got != "https://cdn.example/fanmade/objects/abc/song%20%23%3F.ogg" {
		t.Fatalf("%q %v", got, err)
	}
	for _, key := range []string{"../private", "/private", "objects/../secret", "objects\\secret"} {
		if _, err := s.PublicURL(key); err == nil {
			t.Fatalf("accepted key %q", key)
		}
	}
	s.SetPublicBaseURL("")
	if got, err := s.PublicURL("objects/abc/audio"); got != "" || err != nil {
		t.Fatal(got, err)
	}
}
