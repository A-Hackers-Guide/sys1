// Package marketplace loads, verifies and filters sys1 packages.
//
// A package is a JSON manifest: metadata plus the TaskSpec to run. Packages
// compiled into sys1 are trusted. Any other package must be signed with
// Ed25519 by a key the node owner has chosen to trust; unsigned or
// wrongly signed packages are listed as unverified and cannot be deployed
// until the owner trusts their key. See docs/PACKAGES.md.
package marketplace

import (
	"crypto/ed25519"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

// Input is a value the user supplies at deploy time. ${NAME} in the
// image, command or env values is replaced by it.
type Input struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Secret  bool   `json:"secret,omitempty"`
	Default string `json:"default,omitempty"`
}

type Manifest struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Category    string            `json:"category"`
	Icon        string            `json:"icon,omitempty"`
	Author      string            `json:"author,omitempty"`
	Homepage    string            `json:"homepage,omitempty"`
	Inputs      []Input           `json:"inputs,omitempty"`
	Run         protocol.TaskSpec `json:"run"`
	PublicKey   string            `json:"publicKey,omitempty"` // base64 Ed25519 key that signed it
	Signature   string            `json:"signature,omitempty"` // base64 signature over the canonical form
}

// Package is a manifest plus what the node knows about it.
type Package struct {
	Manifest
	Source   string `json:"source"`   // "builtin" or the file it came from
	Verified bool   `json:"verified"` // builtin, or signed by a trusted key
	Problem  string `json:"problem,omitempty"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func (m Manifest) Validate() error {
	if !nameRe.MatchString(m.Name) {
		return fmt.Errorf("invalid package name %q", m.Name)
	}
	if m.Title == "" || m.Version == "" {
		return errors.New("package needs a title and version")
	}
	for _, in := range m.Inputs {
		if !regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`).MatchString(in.Name) {
			return fmt.Errorf("invalid input name %q", in.Name)
		}
	}
	// Validate with placeholder inputs filled in, since the image itself
	// may come from an input.
	vals := map[string]string{}
	for _, in := range m.Inputs {
		vals[in.Name] = "x"
	}
	return m.Apply(vals).Validate()
}

// Canonical is the byte form a signature covers: the manifest with its
// signature field cleared. encoding/json output is deterministic.
func (m Manifest) Canonical() []byte {
	m.Signature = ""
	b, _ := json.Marshal(m)
	return b
}

// Sign sets PublicKey and Signature.
func (m *Manifest) Sign(priv ed25519.PrivateKey) {
	m.PublicKey = base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, m.Canonical()))
}

// VerifySignature checks the signature against the manifest's own key.
// Whether that key is trusted is a separate question.
func (m Manifest) VerifySignature() error {
	if m.Signature == "" || m.PublicKey == "" {
		return errors.New("package is not signed")
	}
	pub, err := base64.StdEncoding.DecodeString(m.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("package has a malformed public key")
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil || !ed25519.Verify(pub, m.Canonical(), sig) {
		return errors.New("package signature is invalid (it was modified after signing)")
	}
	return nil
}

// Apply substitutes deploy-time input values into a copy of the spec.
func (m Manifest) Apply(values map[string]string) protocol.TaskSpec {
	vals := map[string]string{}
	for _, in := range m.Inputs {
		vals[in.Name] = in.Default
	}
	for k, v := range values {
		vals[k] = v
	}
	sub := func(s string) string {
		for k, v := range vals {
			s = strings.ReplaceAll(s, "${"+k+"}", v)
		}
		return s
	}
	spec := m.Run
	spec.Image = sub(spec.Image)
	spec.Command = append([]string(nil), m.Run.Command...)
	for i := range spec.Command {
		spec.Command[i] = sub(spec.Command[i])
	}
	if m.Run.Env != nil {
		spec.Env = map[string]string{}
		for k, v := range m.Run.Env {
			spec.Env[k] = sub(v)
		}
	}
	return spec
}

// Permissions lists, in plain words, what a package asks to do.
func (m Manifest) Permissions() []string {
	r := m.Run
	var p []string
	if r.Kind == protocol.KindExec {
		p = append(p, "Runs a native program (only on machines marked as yours)")
	} else {
		p = append(p, "Runs in a sandboxed container")
	}
	if r.Network {
		p = append(p, "Uses the network")
	}
	for _, port := range r.Ports {
		p = append(p, "Opens port "+strings.SplitN(port, ":", 2)[0])
	}
	if len(r.Volumes) > 0 {
		p = append(p, "Keeps its data in pooled storage, encrypted, so it moves with the app")
	}
	if len(r.LocalVolumes) > 0 {
		p = append(p, "Keeps large data on the machine it runs on")
	}
	if r.GPU {
		p = append(p, "Uses the GPU when available")
	}
	if r.Service {
		p = append(p, "Runs continuously")
	}
	return p
}

// Machine is the part of a connected machine compatibility depends on.
type Machine struct {
	OS         string
	Containers bool
	Owner      bool
}

// Compatible reports whether any connected machine can run the package,
// and if not, why. Containers run anywhere with a container runtime
// (natively on Linux, in a lightweight VM elsewhere); native programs need
// a machine of the right OS that its owner has allowed them on.
func (m Manifest) Compatible(machines []Machine) (bool, string) {
	if len(machines) == 0 {
		return false, "No machines are connected yet"
	}
	switch m.Run.Kind {
	case protocol.KindContainer:
		for _, x := range machines {
			if x.Containers {
				return true, ""
			}
		}
		return false, "Needs a machine with Docker or Podman"
	case protocol.KindExec:
		for _, x := range machines {
			if !x.Owner {
				continue
			}
			for _, os := range m.Run.OS {
				if os == x.OS || os == "any" {
					return true, ""
				}
			}
		}
		return false, "Needs one of your own machines running " + strings.Join(m.Run.OS, ", ")
	}
	return false, "Unknown package kind"
}

//go:embed catalog/*.json
var builtin embed.FS

// Catalog is every package the node knows about.
type Catalog struct {
	Packages map[string]*Package
}

// Load reads the builtin packages and every *.json in dir (may be empty).
// trustedKeys are base64 Ed25519 public keys.
func Load(dir string, trustedKeys []string) (*Catalog, error) {
	c := &Catalog{Packages: map[string]*Package{}}
	entries, err := builtin.ReadDir("catalog")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		b, _ := builtin.ReadFile("catalog/" + e.Name())
		var m Manifest
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("builtin %s: %w", e.Name(), err)
		}
		if err := m.Validate(); err != nil {
			return nil, fmt.Errorf("builtin %s: %w", e.Name(), err)
		}
		c.Packages[m.Name] = &Package{Manifest: m, Source: "builtin", Verified: true}
	}
	if dir == "" {
		return c, nil
	}
	trusted := map[string]bool{}
	for _, k := range trustedKeys {
		trusted[strings.TrimSpace(k)] = true
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var m Manifest
		p := &Package{Source: filepath.Base(f)}
		if err := json.Unmarshal(b, &m); err != nil {
			continue
		}
		p.Manifest = m
		if _, clash := c.Packages[m.Name]; clash {
			continue // builtin names cannot be shadowed
		}
		switch {
		case m.Validate() != nil:
			p.Problem = m.Validate().Error()
		case m.VerifySignature() != nil:
			p.Problem = m.VerifySignature().Error()
		case !trusted[m.PublicKey]:
			p.Problem = "signed by a key you have not chosen to trust"
		default:
			p.Verified = true
		}
		c.Packages[m.Name] = p
	}
	return c, nil
}

// Sorted returns packages by category, then title.
func (c *Catalog) Sorted() []*Package {
	out := make([]*Package, 0, len(c.Packages))
	for _, p := range c.Packages {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Title < out[j].Title
	})
	return out
}
