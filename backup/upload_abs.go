package backup

import (
	"context"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
)

const blobPrefix = "databases/"

type absTarget struct {
	client    *azblob.Client
	container string
}

func absFromEnv() (*absTarget, error) {
	accountName := os.Getenv("ABS_ACCOUNT_NAME")
	accountKey := os.Getenv("ABS_ACCESS_KEY")
	containerName := os.Getenv("ABS_CONTAINER")
	if accountName == "" || accountKey == "" || containerName == "" {
		return nil, fmt.Errorf("missing required environment variables: ABS_ACCOUNT_NAME, ABS_ACCESS_KEY, ABS_CONTAINER")
	}

	cred, err := azblob.NewSharedKeyCredential(accountName, accountKey)
	if err != nil {
		return nil, fmt.Errorf("create shared key credential: %w", err)
	}
	serviceURL := fmt.Sprintf("https://%s.blob.core.windows.net/", accountName)
	client, err := azblob.NewClientWithSharedKeyCredential(serviceURL, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("create Azure Blob Storage client: %w", err)
	}
	return &absTarget{client: client, container: containerName}, nil
}

func blobName(filePath string) string {
	return blobPrefix + filepath.Base(filePath)
}

// UploadToABS streams filePath to Azure Blob Storage under databases/<basename>.
func UploadToABS(ctx context.Context, filePath string) error {
	target, err := absFromEnv()
	if err != nil {
		return err
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open backup file: %w", err)
	}
	defer file.Close()

	name := blobName(filePath)
	if _, err := target.client.UploadFile(ctx, target.container, name, file, nil); err != nil {
		return fmt.Errorf("upload %s to Azure Blob Storage: %w", name, err)
	}
	log.Printf("File uploaded to Azure Blob Storage: %s", name)
	return nil
}

// blobsToDelete returns the backup blobs that fall outside the keepCount
// newest. Blobs not named like a backup archive are never returned.
// keepCount <= 0 means keep everything.
func blobsToDelete(names []string, keepCount int) []string {
	if keepCount <= 0 {
		return nil
	}
	var archives []string
	for _, name := range names {
		if backupFileRe.MatchString(path.Base(name)) {
			archives = append(archives, name)
		}
	}
	if len(archives) <= keepCount {
		return nil
	}
	sort.Sort(sort.Reverse(sort.StringSlice(archives)))
	return archives[keepCount:]
}

// CleanupRemoteBackups keeps the keepCount newest blobs under databases/.
func CleanupRemoteBackups(ctx context.Context, keepCount int) error {
	target, err := absFromEnv()
	if err != nil {
		return err
	}

	var names []string
	pager := target.client.NewListBlobsFlatPager(target.container, &container.ListBlobsFlatOptions{Prefix: to(blobPrefix)})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list remote backups: %w", err)
		}
		for _, item := range page.Segment.BlobItems {
			if item.Name != nil {
				names = append(names, *item.Name)
			}
		}
	}

	for _, name := range blobsToDelete(names, keepCount) {
		log.Printf("Deleting old remote backup: %s", name)
		if _, err := target.client.DeleteBlob(ctx, target.container, name, nil); err != nil {
			return fmt.Errorf("delete remote backup %s: %w", name, err)
		}
	}
	return nil
}

func to[T any](v T) *T { return &v }
