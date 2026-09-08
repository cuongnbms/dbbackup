package backup

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
)

type absTarget struct {
	client    *azblob.Client
	container string
	// prefix is the folder artifacts are stored under in the container. It also
	// bounds Cleanup, which never lists or deletes outside it.
	prefix string
}

// absFromEnv builds the client from the account credentials in the
// environment. The prefix comes from config rather than the environment,
// because it is layout rather than credentials.
func absFromEnv(prefix string) (*absTarget, error) {
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
	return &absTarget{client: client, container: containerName, prefix: prefix}, nil
}

func (t *absTarget) Name() string { return "Azure Blob Storage" }

// Upload streams filePath to Azure Blob Storage under <prefix><basename>.
func (t *absTarget) Upload(ctx context.Context, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open backup file: %w", err)
	}
	defer file.Close()

	name := remoteKey(t.prefix, filePath)
	if _, err := t.client.UploadFile(ctx, t.container, name, file, nil); err != nil {
		return fmt.Errorf("upload %s: %w", name, err)
	}
	log.Printf("File uploaded to Azure Blob Storage: %s", name)
	return nil
}

// Cleanup keeps the keepCount newest blobs under the target's prefix.
func (t *absTarget) Cleanup(ctx context.Context, keepCount int) error {
	var names []string
	pager := t.client.NewListBlobsFlatPager(t.container, &container.ListBlobsFlatOptions{Prefix: to(t.prefix)})
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

	for _, name := range archivesToDelete(names, keepCount) {
		log.Printf("Deleting old remote backup: %s", name)
		if _, err := t.client.DeleteBlob(ctx, t.container, name, nil); err != nil {
			return fmt.Errorf("delete remote backup %s: %w", name, err)
		}
	}
	return nil
}

func to[T any](v T) *T { return &v }
