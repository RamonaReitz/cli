package create

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cli/cli/api"
	"github.com/cli/cli/internal/ghrepo"
)

func getOrgPublicKey(client *api.Client, host, orgName string) (*PubKey, error) {
	return getPubKey(client, host, fmt.Sprintf("orgs/%s/actions/secrets/public-key", orgName))
}

func getRepoPubKey(client *api.Client, repo ghrepo.Interface) (*PubKey, error) {
	return getPubKey(client, repo.RepoHost(), fmt.Sprintf("repos/%s/actions/secrets/public-key",
		ghrepo.FullName(repo)))
}

type PubKey struct {
	Key     [32]byte
	ID      string
	encoded string
}

func (pk *PubKey) String() string {
	return pk.encoded
}

func NewPubKey(encodedKey, keyID string) (*PubKey, error) {
	pk, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decode public key: %w", err)
	}

	pka := [32]byte{}
	copy(pka[:], pk[0:32])
	return &PubKey{
		Key:     pka,
		ID:      keyID,
		encoded: encodedKey,
	}, nil
}

func getPubKey(client *api.Client, host, path string) (*PubKey, error) {
	result := struct {
		Key string
		ID  string `json:"key_id"`
	}{}

	err := client.REST(host, "GET", path, nil, &result)
	if err != nil {
		return nil, err
	}

	if result.Key == "" {
		return nil, fmt.Errorf("failed to find public key at %s/%s", host, path)
	}

	return NewPubKey(result.Key, result.ID)
}

type SecretPayload struct {
	EncryptedValue string `json:"encrypted_value"`
	Visibility     string `json:"visibility,omitempty"`
	Repositories   []int  `json:"selected_repository_ids,omitempty"`
	KeyID          string `json:"key_id"`
}

func putOrgSecret(client *api.Client, pk *PubKey, host string, opts CreateOptions, eValue string) error {
	secretName := opts.SecretName
	orgName := opts.OrgName
	visibility := opts.Visibility

	var repositoryIDs []int
	var err error
	if orgName != "" && visibility == visSelected {
		repositoryIDs, err = mapRepoNameToID(client, host, orgName, opts.RepositoryNames)
		if err != nil {
			return fmt.Errorf("failed to look up IDs for repositories %v: %w", opts.RepositoryNames, err)
		}
	}

	payload := SecretPayload{
		EncryptedValue: eValue,
		KeyID:          pk.ID,
		Repositories:   repositoryIDs,
		Visibility:     visibility,
	}
	path := fmt.Sprintf("orgs/%s/actions/secrets/%s", orgName, secretName)

	return putSecret(client, host, path, payload)
}

func putRepoSecret(client *api.Client, pk *PubKey, repo ghrepo.Interface, secretName, eValue string) error {
	payload := SecretPayload{
		EncryptedValue: eValue,
		KeyID:          pk.ID,
	}
	path := fmt.Sprintf("repos/%s/actions/secrets/%s", ghrepo.FullName(repo), secretName)
	return putSecret(client, repo.RepoHost(), path, payload)
}

func putSecret(client *api.Client, host, path string, payload SecretPayload) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to serialize: %w", err)
	}
	requestBody := bytes.NewReader(payloadBytes)

	return client.REST(host, "PUT", path, requestBody, nil)
}

func mapRepoNameToID(client *api.Client, host, orgName string, repositoryNames []string) ([]int, error) {
	queries := make([]string, 0, len(repositoryNames))
	for _, repoName := range repositoryNames {
		queries = append(queries, fmt.Sprintf(`
			%s: repository(owner: %q, name :%q) {
				databaseId
			}
		`, repoName, orgName, repoName))
	}

	query := fmt.Sprintf(`query MapRepositoryNames { %s }`, strings.Join(queries, ""))

	graphqlResult := make(map[string]*struct {
		DatabaseID int `json:"databaseId"`
	})

	err := client.GraphQL(host, query, nil, &graphqlResult)

	gqlErr, isGqlErr := err.(*api.GraphQLErrorResponse)
	if isGqlErr {
		for _, ge := range gqlErr.Errors {
			if ge.Type == "NOT_FOUND" {
				return nil, fmt.Errorf("could not find %s/%s", orgName, ge.Path[0])
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to look up repositories: %w", err)
	}

	result := make([]int, 0, len(repositoryNames))

	for _, repoName := range repositoryNames {
		result = append(result, graphqlResult[repoName].DatabaseID)
	}

	return result, nil
}
