//go:build !windows

/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package bindir

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Currently, these tests don't run on Windows due to dependency on Bash.

func newBinDir(t *testing.T, scripts ...string) string {
	t.Helper()
	binDir := t.TempDir()

	for i, s := range scripts {
		err := os.WriteFile(
			path.Join(binDir, fmt.Sprintf("%v.sh", i)),
			[]byte(strings.TrimSpace(s)),
			0700,
		)
		require.NoError(t, err)
	}

	return binDir
}

func TestBinDirVerifyImage(t *testing.T) {
	t.Run("proper input/output management", func(t *testing.T) {
		outDir := t.TempDir()
		argsFile := path.Join(outDir, "args.txt")
		stdinFile := path.Join(outDir, "stdin.txt")

		binDir := newBinDir(t, fmt.Sprintf(`
#!/usr/bin/env bash
set -euf -o pipefail
echo -n $@ > %v
cat - > %v
echo Reason A line 1
echo Debug A line 1 1>&2
echo Reason A line 2
echo Debug A line 2 1>&2
exit 0
			`, argsFile, stdinFile),
		)

		v := NewImageVerifier(&Config{
			BinDir:             binDir,
			MaxVerifiers:       -1,
			PerVerifierTimeout: 1 * time.Second,
		})

		j, err := v.VerifyImage(context.Background(), "registry.example.com/image:abc", ocispec.Descriptor{
			Digest:      "sha256:98ea6e4f216f2fb4b69fff9b3a44842c38686ca685f3f55dc48c5d3fb1107be4",
			MediaType:   "application/vnd.docker.distribution.manifest.list.v2+json",
			Size:        2048,
			Annotations: map[string]string{"a": "b"},
		})
		assert.NoError(t, err)
		assert.True(t, j.OK)
		assert.Equal(t, "0.sh => Reason A line 1\nReason A line 2", j.Reason)

		b, err := os.ReadFile(argsFile)
		require.NoError(t, err)
		assert.Equal(t, "-name registry.example.com/image:abc -digest sha256:98ea6e4f216f2fb4b69fff9b3a44842c38686ca685f3f55dc48c5d3fb1107be4 -stdin-media-type application/vnd.oci.descriptor.v1+json", string(b))

		b, err = os.ReadFile(stdinFile)
		require.NoError(t, err)
		assert.Equal(t, `{"mediaType":"application/vnd.docker.distribution.manifest.list.v2+json","digest":"sha256:98ea6e4f216f2fb4b69fff9b3a44842c38686ca685f3f55dc48c5d3fb1107be4","size":2048,"annotations":{"a":"b"}}`, strings.TrimSpace(string(b)))
	})

	t.Run("stdout truncation", func(t *testing.T) {
		binDir := newBinDir(t, `
#!/usr/bin/env bash
set -euf -o pipefail
head -c 50000 /dev/random
exit 0
			`,
		)

		v := NewImageVerifier(&Config{
			BinDir:             binDir,
			MaxVerifiers:       -1,
			PerVerifierTimeout: 1 * time.Second,
		})

		j, err := v.VerifyImage(context.Background(), "registry.example.com/image:abc", ocispec.Descriptor{})
		assert.NoError(t, err)
		assert.True(t, j.OK)
		assert.Less(t, len(j.Reason), outputLimitBytes+1024)
	})

	t.Run("missing directory", func(t *testing.T) {
		v := NewImageVerifier(&Config{
			BinDir:             path.Join(t.TempDir(), "missing_directory"),
			MaxVerifiers:       10,
			PerVerifierTimeout: 1 * time.Second,
		})

		j, err := v.VerifyImage(context.Background(), "registry.example.com/image:abc", ocispec.Descriptor{})
		assert.NoError(t, err)
		assert.True(t, j.OK)
		assert.NotEmpty(t, j.Reason)
	})

	t.Run("empty directory", func(t *testing.T) {
		v := NewImageVerifier(&Config{
			BinDir:             t.TempDir(),
			MaxVerifiers:       10,
			PerVerifierTimeout: 1 * time.Second,
		})

		j, err := v.VerifyImage(context.Background(), "registry.example.com/image:abc", ocispec.Descriptor{})
		assert.NoError(t, err)
		assert.True(t, j.OK)
		assert.NotEmpty(t, j.Reason)
	})

	t.Run("max verifiers = 0", func(t *testing.T) {
		binDir := newBinDir(t, `
#!/usr/bin/env bash
# This isn't called.
exit 1
			`,
		)

		v := NewImageVerifier(&Config{
			BinDir:             binDir,
			MaxVerifiers:       0,
			PerVerifierTimeout: 1 * time.Second,
		})

		j, err := v.VerifyImage(context.Background(), "registry.example.com/image:abc", ocispec.Descriptor{})
		assert.NoError(t, err)
		assert.True(t, j.OK)
		assert.Empty(t, j.Reason)
	})

	t.Run("max verifiers = 1", func(t *testing.T) {
		binDir := newBinDir(t, `
#!/usr/bin/env bash
exit 0
			`,
			`
#!/usr/bin/env bash
# This isn't called.
exit 1
			`,
		)

		v := NewImageVerifier(&Config{
			BinDir:             binDir,
			MaxVerifiers:       1,
			PerVerifierTimeout: 1 * time.Second,
		})

		j, err := v.VerifyImage(context.Background(), "registry.example.com/image:abc", ocispec.Descriptor{})
		assert.NoError(t, err)
		assert.True(t, j.OK)
		assert.NotEmpty(t, j.Reason)
	})

	t.Run("max verifiers = 2", func(t *testing.T) {
		binDir := newBinDir(t, `
#!/usr/bin/env bash
exit 0
			`,
			`
#!/usr/bin/env bash
exit 0
			`,
			`
#!/usr/bin/env bash
# This isn't called.
exit 1
			`,
		)

		v := NewImageVerifier(&Config{
			BinDir:             binDir,
			MaxVerifiers:       2,
			PerVerifierTimeout: 1 * time.Second,
		})

		j, err := v.VerifyImage(context.Background(), "registry.example.com/image:abc", ocispec.Descriptor{})
		assert.NoError(t, err)
		assert.True(t, j.OK)
		assert.NotEmpty(t, j.Reason)
	})

	t.Run("max verifiers = 2", func(t *testing.T) {
		binDir := newBinDir(t, `
#!/usr/bin/env bash
exit 0
			`,
			`
#!/usr/bin/env bash
exit 0
			`,
			`
#!/usr/bin/env bash
# This isn't called.
exit 1
			`,
		)

		v := NewImageVerifier(&Config{
			BinDir:             binDir,
			MaxVerifiers:       2,
			PerVerifierTimeout: 1 * time.Second,
		})

		j, err := v.VerifyImage(context.Background(), "registry.example.com/image:abc", ocispec.Descriptor{})
		assert.NoError(t, err)
		assert.True(t, j.OK)
		assert.NotEmpty(t, j.Reason)
	})

	t.Run("max verifiers = 3, all accept", func(t *testing.T) {
		binDir := newBinDir(t, `
#!/usr/bin/env bash
echo Reason A
exit 0
			`,
			`
#!/usr/bin/env bash
echo Reason B
exit 0
			`,
			`
#!/usr/bin/env bash
echo Reason C
exit 0
			`,
		)

		v := NewImageVerifier(&Config{
			BinDir:             binDir,
			MaxVerifiers:       3,
			PerVerifierTimeout: 1 * time.Second,
		})

		j, err := v.VerifyImage(context.Background(), "registry.example.com/image:abc", ocispec.Descriptor{})
		assert.NoError(t, err)
		assert.True(t, j.OK)
		assert.Equal(t, "0.sh => Reason A, 1.sh => Reason B, 2.sh => Reason C", j.Reason)
	})

	t.Run("max verifiers = 3, with reject", func(t *testing.T) {
		binDir := newBinDir(t, `
#!/usr/bin/env bash
echo Reason A
exit 0
			`,
			`
#!/usr/bin/env bash
echo Reason B
exit 0
			`,
			`
#!/usr/bin/env bash
echo Reason C
exit 1
			`,
		)

		v := NewImageVerifier(&Config{
			BinDir:             binDir,
			MaxVerifiers:       3,
			PerVerifierTimeout: 1 * time.Second,
		})

		j, err := v.VerifyImage(context.Background(), "registry.example.com/image:abc", ocispec.Descriptor{})
		assert.NoError(t, err)
		assert.False(t, j.OK)
		assert.Equal(t, "verifier 2.sh rejected image (exit code 1): Reason C", j.Reason)
	})

	t.Run("max verifiers = -1, all accept", func(t *testing.T) {
		binDir := newBinDir(t, `
#!/usr/bin/env bash
echo Reason A
exit 0
			`,
			`
#!/usr/bin/env bash
echo Reason B
exit 0
			`,
			`
#!/usr/bin/env bash
echo Reason C
exit 0
			`,
		)

		v := NewImageVerifier(&Config{
			BinDir:             binDir,
			MaxVerifiers:       -1,
			PerVerifierTimeout: 1 * time.Second,
		})

		j, err := v.VerifyImage(context.Background(), "registry.example.com/image:abc", ocispec.Descriptor{})
		assert.NoError(t, err)
		assert.True(t, j.OK)
		assert.Equal(t, "0.sh => Reason A, 1.sh => Reason B, 2.sh => Reason C", j.Reason)
	})

	t.Run("max verifiers = -1, with reject", func(t *testing.T) {
		binDir := newBinDir(t, `
#!/usr/bin/env bash
echo Reason A
exit 0
			`,
			`
#!/usr/bin/env bash
echo Reason B
exit 0
			`,
			`
#!/usr/bin/env bash
echo Reason C
exit 1
			`,
		)

		v := NewImageVerifier(&Config{
			BinDir:             binDir,
			MaxVerifiers:       -1,
			PerVerifierTimeout: 1 * time.Second,
		})

		j, err := v.VerifyImage(context.Background(), "registry.example.com/image:abc", ocispec.Descriptor{})
		assert.NoError(t, err)
		assert.False(t, j.OK)
		assert.Equal(t, "verifier 2.sh rejected image (exit code 1): Reason C", j.Reason)
	})

	t.Run("max verifiers = -1, with timeout", func(t *testing.T) {
		binDir := newBinDir(t, `
#!/usr/bin/env bash
exit 0
			`,
			`
#!/usr/bin/env bash
exit 0
			`,
			`
#!/usr/bin/env bash
# Greater than 250ms PerVerifierTimeout.
sleep 1000
exit 0
			`,
		)

		v := NewImageVerifier(&Config{
			BinDir:             binDir,
			MaxVerifiers:       -1,
			PerVerifierTimeout: 250 * time.Millisecond,
		})

		j, err := v.VerifyImage(context.Background(), "registry.example.com/image:abc", ocispec.Descriptor{})
		assert.Error(t, err)
		assert.Nil(t, j)
	})

	t.Run("max verifiers = -1, with exec failure", func(t *testing.T) {
		binDir := newBinDir(t, `
#!/usr/bin/env bash
exit 0
			`,
			`
#!/usr/bin/env bash
exit 0
			`,
			`
#!/badshell
exit 0
			`,
		)

		v := NewImageVerifier(&Config{
			BinDir:             binDir,
			MaxVerifiers:       -1,
			PerVerifierTimeout: 1 * time.Second,
		})

		j, err := v.VerifyImage(context.Background(), "registry.example.com/image:abc", ocispec.Descriptor{})
		assert.Error(t, err)
		assert.Nil(t, j)
	})

	t.Run("descriptor larger than linux pipe buffer, verifier doesn't read stdin", func(t *testing.T) {
		binDir := newBinDir(t, `
#!/usr/bin/env bash
exit 0
			`,
		)

		v := NewImageVerifier(&Config{
			BinDir:             binDir,
			MaxVerifiers:       1,
			PerVerifierTimeout: 10 * time.Second,
		})

		j, err := v.VerifyImage(context.Background(), "registry.example.com/image:abc", ocispec.Descriptor{
			Digest:    "sha256:98ea6e4f216f2fb4b69fff9b3a44842c38686ca685f3f55dc48c5d3fb1107be4",
			MediaType: "application/vnd.docker.distribution.manifest.list.v2+json",
			Size:      2048,
			Annotations: map[string]string{
				// Pipe buffer is usually 64KiB.
				"large_payload": strings.Repeat("0", 2*64*(2<<9)),
			},
		})

		// Should see a log like the following, but verification still succeeds:
		// time="2023-09-05T11:15:50-04:00" level=warning msg="failed to completely write descriptor to stdin" error="write |1: broken pipe"

		assert.NoError(t, err)
		assert.True(t, j.OK)
		assert.NotEmpty(t, j.Reason)
	})
}
