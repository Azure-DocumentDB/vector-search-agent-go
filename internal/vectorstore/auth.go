package vectorstore

import (
	"context"
	"crypto/tls"
	"errors"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/safety"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"time"
)

func oidcCallback(credential azcore.TokenCredential) options.OIDCCallback {
	checked := safety.CheckedCredential{Inner: credential, Scope: safety.MongoScope}
	return func(ctx context.Context, _ *options.OIDCArgs) (*options.OIDCCredential, error) {
		token, err := checked.GetToken(ctx, policy.TokenRequestOptions{})
		if err != nil {
			return nil, err
		}
		return &options.OIDCCredential{AccessToken: token.Token, ExpiresAt: &token.ExpiresOn}, nil
	}
}
func configureOptions(opts *options.ClientOptions, c VectorStoreConfig, credential azcore.TokenCredential) (*options.ClientOptions, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	opts.SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}).SetConnectTimeout(15 * time.Second).SetServerSelectionTimeout(20 * time.Second).SetTimeout(30 * time.Second).SetMaxPoolSize(5).SetRetryWrites(false).SetRetryReads(false)
	if c.AuthMode == "entra" {
		if credential == nil {
			return nil, errors.New("Entra credential required")
		}
		opts.SetAuth(options.Credential{AuthMechanism: "MONGODB-OIDC", AuthSource: "$external", Username: c.OIDCUsername, OIDCMachineCallback: oidcCallback(credential)})
	} else {
		if opts.Auth == nil {
			return nil, errors.New("native credentials required")
		}
		opts.Auth.AuthMechanism = "SCRAM-SHA-256"
		opts.Auth.AuthSource = "admin"
	}
	if err := opts.Validate(); err != nil {
		return nil, safety.Protect("MongoDB options validation", err)
	}
	return opts, nil
}
func NewVectorStore(ctx context.Context, c *VectorStoreConfig) (*VectorStore, error) {
	return newVectorStore(ctx, c, func() (azcore.TokenCredential, error) { return azidentity.NewDefaultAzureCredential(nil) })
}
func newVectorStore(ctx context.Context, c *VectorStoreConfig, factory func() (azcore.TokenCredential, error)) (*VectorStore, error) {
	if c == nil {
		return nil, errors.New("database configuration required")
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	var credential azcore.TokenCredential
	if c.AuthMode == "entra" {
		var err error
		credential, err = factory()
		if err != nil {
			return nil, safety.Protect("database credential initialization", err)
		}
	}
	uri := c.ConnectionString
	if c.AuthMode == "entra" {
		uri = "mongodb+srv://" + c.Host + "/"
	}
	opts, err := configureOptions(options.Client().ApplyURI(uri), *c, credential)
	if err != nil {
		return nil, err
	}
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, safety.Protect("database connection", err)
	}
	store := &VectorStore{config: *c, client: client, database: client.Database(c.DatabaseName), collection: client.Database(c.DatabaseName).Collection(c.CollectionName)}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, errors.Join(safety.Protect("database authentication/ping", err), store.Close())
	}
	return store, nil
}
func (vs *VectorStore) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := vs.client.Disconnect(ctx); err != nil {
		return safety.Protect("database disconnect", err)
	}
	return nil
}
