package backup

import "testing"

func TestABSFromEnvUsesTheConfiguredContainerAndPrefix(t *testing.T) {
	enableABS(t)

	target, err := absFromEnv("myproject/dumps/")
	if err != nil {
		t.Fatal(err)
	}
	if target.container != "backups" {
		t.Fatalf("got container %q", target.container)
	}
	if target.prefix != "myproject/dumps/" {
		t.Fatalf("got prefix %q", target.prefix)
	}
}

func TestABSFromEnvNeedsItsCredentials(t *testing.T) {
	enableABS(t)
	t.Setenv("ABS_ACCESS_KEY", "")

	if _, err := absFromEnv("databases/"); err == nil {
		t.Fatal("expected an error when the access key is missing")
	}
}
