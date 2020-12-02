package create

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/api"
	"github.com/cli/cli/internal/ghinstance"
	"github.com/cli/cli/internal/ghrepo"
	"github.com/cli/cli/pkg/cmdutil"
	"github.com/cli/cli/pkg/iostreams"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/nacl/box"
)

type CreateOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)

	SecretName      string
	OrgName         string
	Body            string
	Visibility      string
	RepositoryNames []string
}

func NewCmdCreate(f *cmdutil.Factory, runF func(*CreateOptions) error) *cobra.Command {
	opts := &CreateOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
	}

	cmd := &cobra.Command{
		Use:   "create <secret name>",
		Short: "Create secrets",
		Long:  "Locally encrypt a new secret and send it to GitHub for storage.",
		Example: heredoc.Doc(`
			$ cat SECRET.txt | gh secret create NEW_SECRET
			$ gh secret create NEW_SECRET -b"some literal value"
			$ gh secret create NEW_SECRET -b"@file.json"
			$ gh secret create ORG_SECRET --org=myOrg --visibility="repo1,repo2,repo3"
			$ gh secret create ORG_SECRET --org=myOrg --visibility="all"
`),
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return &cmdutil.FlagError{Err: errors.New("must pass single secret name")}
			}
			if !cmd.Flags().Changed("body") && opts.IO.IsStdinTTY() {
				return &cmdutil.FlagError{Err: errors.New("no --body specified but nothing on STIDN")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo

			opts.SecretName = args[0]

			if cmd.Flags().Changed("visibility") {
				if opts.OrgName == "" {
					return &cmdutil.FlagError{Err: errors.New("--visibility not supported for repository secrets; did you mean to pass --org?")}
				}
				if opts.Visibility != "all" && opts.Visibility != "private" {
					opts.RepositoryNames = strings.Split(opts.Visibility, ",")
				}
			}

			if runF != nil {
				return runF(opts)
			}

			return createRun(opts)
		},
	}
	cmd.Flags().StringVarP(&opts.OrgName, "org", "o", "", "List secrets for an organization")
	cmd.Flags().Lookup("org").NoOptDefVal = "@owner"
	cmd.Flags().StringVarP(&opts.Visibility, "visibility", "v", "private", "Set visibility for an organization secret.")
	cmd.Flags().StringVarP(&opts.Body, "body", "b", "-", "Provide either a literal string or a file path; prepend file paths with an @. Reads from STDIN if not provided.")

	return cmd
}

func createRun(opts *CreateOptions) error {
	body, err := getBody(opts)
	if err != nil {
		return fmt.Errorf("did not understand secret body: %w", err)
	}

	orgName := opts.OrgName

	c, err := opts.HttpClient()
	if err != nil {
		return fmt.Errorf("could not create http client: %w", err)
	}
	client := api.NewClientFromHTTP(c)

	var baseRepo ghrepo.Interface
	if orgName == "" || orgName == "@owner" {
		baseRepo, err = opts.BaseRepo()
		if err != nil {
			return fmt.Errorf("could not determine base repo: %w", err)
		}
	}

	host := ghinstance.OverridableDefault()
	if orgName == "@owner" {
		orgName = baseRepo.RepoOwner()
		host = baseRepo.RepoHost()
	}

	// TODO need key_id for the eventual push, so this needs to be restructured.
	var pk string
	if orgName != "" {
		pk, err = getOrgPublicKey(client, host, orgName)
	} else {
		pk, err = getRepoPubKey(client, baseRepo)
	}
	if err != nil {
		return fmt.Errorf("failed to fetch public key: %w", err)
	}

	pubKey, err := base64.StdEncoding.DecodeString(pk)
	if err != nil {
		return fmt.Errorf("failed to decode public key: %w", err)
	}

	pka := [32]byte{}
	copy(pka[:], pubKey[0:32])

	eBody, err := box.SealAnonymous(nil, body, &pka, nil)
	if err != nil {
		return fmt.Errorf("failed to encrypt body: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(eBody)

	if orgName != "" {
		// TODO support visibility
		err = putOrgSecret(client, host, opts.SecretName, encoded)
	} else {
		err = putRepoSecret(client, baseRepo, opts.SecretName, encoded)
	}
	if err != nil {
		return fmt.Errorf("failed to create secret: %w", err)
	}

	return nil
}

func getBody(opts *CreateOptions) (body []byte, err error) {
	if opts.Body == "-" {
		body, err = ioutil.ReadAll(opts.IO.In)
		if err != nil {
			return nil, fmt.Errorf("failed to read from STDIN: %w", err)
		}

		return
	}

	if strings.HasPrefix(opts.Body, "@") {
		body, err = opts.IO.ReadUserFile(opts.Body[1:])
		if err != nil {
			return nil, fmt.Errorf("failed to read file %s: %w", opts.Body[1:], err)
		}

		return
	}

	return []byte(opts.Body), nil
}
