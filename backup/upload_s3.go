package backup

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type s3Target struct {
	client *s3.Client
	bucket string
	// pathStyle records the addressing the client was built with, so a test
	// can tell that a custom endpoint switched it on.
	pathStyle bool
}

// s3FromEnv builds the S3 client from the standard AWS chain: environment
// variables, the shared config files, or the instance/task role. Only the
// bucket is ours to ask for, which is what lets the container run on an IAM
// role with no static keys at all.
//
// S3_ENDPOINT points at an S3-compatible store (MinIO, Ceph, R2). Those serve
// buckets as a path rather than a subdomain, so it also switches the client to
// path-style addressing.
func s3FromEnv() (*s3Target, error) {
	bucket := os.Getenv("S3_BUCKET")
	if bucket == "" {
		return nil, fmt.Errorf("missing required environment variable: S3_BUCKET")
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	// Without a region the SDK still builds a client and only fails on the
	// first call, which would be halfway through a backup run.
	if cfg.Region == "" {
		return nil, fmt.Errorf("missing required environment variable: AWS_REGION")
	}

	endpoint := os.Getenv("S3_ENDPOINT")
	pathStyle := endpoint != ""
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		}
	})
	return &s3Target{client: client, bucket: bucket, pathStyle: pathStyle}, nil
}

func (t *s3Target) Name() string { return "AWS S3" }

// Upload streams filePath to the bucket under databases/<basename>. The
// manager uploads in parts, so an archive past the 5 GB single-PUT limit still
// goes up, and a failed part is retried on its own.
func (t *s3Target) Upload(ctx context.Context, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open backup file: %w", err)
	}
	defer file.Close()

	key := remoteKey(filePath)
	uploader := manager.NewUploader(t.client)
	if _, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(t.bucket),
		Key:    aws.String(key),
		Body:   file,
	}); err != nil {
		return fmt.Errorf("upload %s: %w", key, err)
	}
	log.Printf("File uploaded to AWS S3: s3://%s/%s", t.bucket, key)
	return nil
}

// Cleanup keeps the keepCount newest objects under databases/.
func (t *s3Target) Cleanup(ctx context.Context, keepCount int) error {
	var keys []string
	pager := s3.NewListObjectsV2Paginator(t.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(t.bucket),
		Prefix: aws.String(remotePrefix),
	})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list remote backups: %w", err)
		}
		for _, obj := range page.Contents {
			if obj.Key != nil {
				keys = append(keys, *obj.Key)
			}
		}
	}

	for _, key := range archivesToDelete(keys, keepCount) {
		log.Printf("Deleting old remote backup: s3://%s/%s", t.bucket, key)
		if _, err := t.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(t.bucket),
			Key:    aws.String(key),
		}); err != nil {
			return fmt.Errorf("delete remote backup %s: %w", key, err)
		}
	}
	return nil
}
