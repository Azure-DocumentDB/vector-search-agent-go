package vectorstore

import (
	"errors"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/safety"
	"os"
	"regexp"
	"strconv"
)

const CollectionName = "agent_hotels"
const IndexName = "agent_vector_index"
const Owner = "vector-search-agent-go/v1"
const LiveDatabase = "agent_go_disposable_live_gate"

var databaseName = regexp.MustCompile(`^agent_go_disposable_[a-z0-9_]{1,32}$`)

type VectorStoreConfig struct {
	Host, ConnectionString, AuthMode, OIDCUsername                string
	DatabaseName, CollectionName, DisposableTarget, CleanupTarget string
	Algorithm                                                     string
	Dimensions                                                    int
	AllowWrites                                                   bool
}

func LoadConfigFromEnv() (*VectorStoreConfig, error) {
	c := &VectorStoreConfig{Host: os.Getenv("AZURE_DOCUMENTDB_HOST"), ConnectionString: os.Getenv("AZURE_DOCUMENTDB_CONNECTION_STRING"), AuthMode: os.Getenv("AZURE_DOCUMENTDB_AUTH_MODE"), OIDCUsername: os.Getenv("AZURE_DOCUMENTDB_OIDC_USERNAME"), DatabaseName: os.Getenv("AZURE_DOCUMENTDB_DATABASENAME"), CollectionName: os.Getenv("AZURE_DOCUMENTDB_COLLECTION"), DisposableTarget: os.Getenv("AGENT_DISPOSABLE_TARGET"), CleanupTarget: os.Getenv("AGENT_CONFIRM_CLEANUP"), Algorithm: os.Getenv("VECTOR_INDEX_ALGORITHM"), Dimensions: 1536}
	if c.Algorithm == "" {
		c.Algorithm = "vector-ivf"
	}
	if v := os.Getenv("EMBEDDING_DIMENSIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, errors.New("EMBEDDING_DIMENSIONS must be an integer")
		}
		c.Dimensions = n
	}
	switch os.Getenv("AGENT_ALLOW_WRITES") {
	case "true":
		c.AllowWrites = true
	case "", "false":
	default:
		return nil, errors.New("AGENT_ALLOW_WRITES must be true or false")
	}
	if os.Getenv("USE_PASSWORDLESS") != "" || os.Getenv("EMBEDDED_FIELD") != "" || os.Getenv("AZURE_DOCUMENTDB_INDEX_NAME") != "" {
		return nil, errors.New("remove legacy USE_PASSWORDLESS, EMBEDDED_FIELD and AZURE_DOCUMENTDB_INDEX_NAME; use independent auth modes and fixed fields/index")
	}
	for _, key := range []string{"IVF_NUM_LISTS", "HNSW_M", "HNSW_EF_CONSTRUCTION", "DISKANN_MAX_DEGREE", "DISKANN_L_BUILD", "VECTOR_SIMILARITY"} {
		if os.Getenv(key) != "" {
			return nil, errors.New("custom vector tuning is unsupported; remove legacy tuning variables")
		}
	}
	return c, c.Validate()
}
func (c VectorStoreConfig) Target() string { return c.DatabaseName + "." + c.CollectionName }
func (c VectorStoreConfig) Validate() error {
	if !databaseName.MatchString(c.DatabaseName) || c.CollectionName != CollectionName || c.DisposableTarget != c.Target() {
		return errors.New("explicit disposable scope required: database agent_go_disposable_<run>, collection agent_hotels, and matching AGENT_DISPOSABLE_TARGET=<database>.agent_hotels")
	}
	if !safety.MongoHost(c.Host) {
		return errors.New("AZURE_DOCUMENTDB_HOST must be an exact public Azure DocumentDB hostname")
	}
	if c.Dimensions < 1 || c.Dimensions > 2000 {
		return errors.New("EMBEDDING_DIMENSIONS must be 1-2000")
	}
	if c.Algorithm != "vector-ivf" && c.Algorithm != "vector-hnsw" && c.Algorithm != "vector-diskann" {
		return errors.New("unsupported VECTOR_INDEX_ALGORITHM")
	}
	switch c.AuthMode {
	case "entra":
		if c.ConnectionString != "" {
			return errors.New("unset native connection string for entra mode; no fallback")
		}
	case "native":
		if c.OIDCUsername != "" {
			return errors.New("unset OIDC username for native mode")
		}
		return safety.NativeURI(c.ConnectionString, c.Host)
	default:
		return errors.New("AZURE_DOCUMENTDB_AUTH_MODE must explicitly be native or entra")
	}
	return nil
}
func (c VectorStoreConfig) Writes() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if !c.AllowWrites {
		return errors.New("writes disabled; require AGENT_ALLOW_WRITES=true")
	}
	return nil
}
func (c VectorStoreConfig) Cleanup() error {
	if err := c.Writes(); err != nil {
		return err
	}
	if c.CleanupTarget != c.Target() {
		return errors.New("cleanup requires AGENT_CONFIRM_CLEANUP matching the exact disposable target")
	}
	return nil
}
