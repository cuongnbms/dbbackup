package backup

import (
	"path/filepath"
	"strings"
	"testing"
)

// isolateAWSConfig stops the SDK from reading the developer's own ~/.aws
// files, which would otherwise supply a region the test is trying to omit.
func isolateAWSConfig(t *testing.T) {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "none")
	t.Setenv("AWS_CONFIG_FILE", missing)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", missing)
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("S3_BUCKET", "")
	t.Setenv("S3_ENDPOINT", "")
}

func TestS3FromEnvNeedsABucket(t *testing.T) {
	isolateAWSConfig(t)
	t.Setenv("AWS_REGION", "ap-southeast-1")

	_, err := s3FromEnv("databases/")
	if err == nil || !strings.Contains(err.Error(), "S3_BUCKET") {
		t.Fatalf("expected an error naming S3_BUCKET, got %v", err)
	}
}

// Without a region the SDK builds a client that only fails on the first call,
// halfway through a backup run. Catching it here names the missing variable.
func TestS3FromEnvNeedsARegion(t *testing.T) {
	isolateAWSConfig(t)
	t.Setenv("S3_BUCKET", "my-backups")

	_, err := s3FromEnv("databases/")
	if err == nil || !strings.Contains(err.Error(), "AWS_REGION") {
		t.Fatalf("expected an error naming AWS_REGION, got %v", err)
	}
}

func TestS3FromEnvUsesTheConfiguredBucket(t *testing.T) {
	isolateAWSConfig(t)
	t.Setenv("S3_BUCKET", "my-backups")
	t.Setenv("AWS_REGION", "ap-southeast-1")

	target, err := s3FromEnv("databases/")
	if err != nil {
		t.Fatal(err)
	}
	if target.bucket != "my-backups" {
		t.Fatalf("got bucket %q", target.bucket)
	}
	if target.prefix != "databases/" {
		t.Fatalf("got prefix %q", target.prefix)
	}
	if !strings.Contains(target.Name(), "S3") {
		t.Fatalf("the name reaches notifications, it should say S3: %q", target.Name())
	}
}

// S3-compatible stores (MinIO, Ceph) serve buckets as a path, not a subdomain,
// so an endpoint override must also switch the client to path-style.
func TestS3FromEnvUsesPathStyleWithACustomEndpoint(t *testing.T) {
	isolateAWSConfig(t)
	t.Setenv("S3_BUCKET", "my-backups")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("S3_ENDPOINT", "http://minio:9000")

	target, err := s3FromEnv("databases/")
	if err != nil {
		t.Fatal(err)
	}
	if !target.pathStyle {
		t.Fatal("a custom endpoint must use path-style addressing")
	}
}
