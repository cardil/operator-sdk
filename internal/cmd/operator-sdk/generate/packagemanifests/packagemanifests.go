// Copyright 2020 The Operator-SDK Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package packagemanifests

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blang/semver/v4"
	metricsannotations "github.com/operator-framework/operator-sdk/internal/annotations/metrics"
	genutil "github.com/operator-framework/operator-sdk/internal/cmd/operator-sdk/generate/internal"
	gencsv "github.com/operator-framework/operator-sdk/internal/generate/clusterserviceversion"
	"github.com/operator-framework/operator-sdk/internal/generate/clusterserviceversion/bases"
	"github.com/operator-framework/operator-sdk/internal/generate/collector"
	genpkg "github.com/operator-framework/operator-sdk/internal/generate/packagemanifest"
)

const (
	longHelp = `
This command generates a set of manifests in a versioned directory and a package manifest file for
your operator. Each versioned directory consists of a ClusterServiceVersion (CSV), CustomResourceDefinitions (CRDs),
and manifests not part of the CSV but required by the operator.

A CSV manifest is generated by collecting data from the set of manifests passed to this command (see below),
such as CRDs, RBAC, etc., and applying that data to a "base" CSV manifest. This base CSV can contain metadata,
added by hand or by the 'generate kustomize manifests' command, and can be passed in like any other manifest
(see below) or by file at the exact path '<kustomize-dir>/bases/<package-name>.clusterserviceversion.yaml'.
Be aware that 'generate packagemanifests' idempotently regenerates a packagemanifests directory,
so all non-metadata values in a base will be overwritten. If no base was passed in, input manifest data
will be applied to an empty CSV.

There are two ways to pass the to-be-packaged set of manifests to this command: stdin via a Unix pipe,
or in a directory using '--input-dir'. See command help for more information on these modes.
Passing a directory is useful for running 'generate packagemanifests' outside of a project or within a project
that does not use kustomize and/or contains cluster-ready manifests on disk.

Set '--version' to supply a semantic version for your new package.

More information on the package manifests format:
https://github.com/operator-framework/operator-registry/#manifest-format
`

	examples = `
  # If running within a project, make sure a 'config' kustomize directory exists and has a 'config/manifests':
  $ tree config/manifests
  config/manifests
  ├── bases
  │   └── memcached-operator.clusterserviceversion.yaml
  └── kustomization.yaml

  # Generate a 0.0.1 packagemanifests by passing manifests to stdin:
  $ kustomize build config/manifests | operator-sdk generate packagemanifests --version 0.0.1
  Generating package manifests version 0.0.1
  ...

  # If running outside of a project, make sure cluster-ready manifests are available on disk:
  $ tree deploy/
  deploy/
  ├── crds
  │   └── cache.my.domain_memcacheds.yaml
  ├── deployment.yaml
  ├── role.yaml
  ├── role_binding.yaml
  ├── service_account.yaml
  └── webhooks.yaml

  # Generate a 0.0.1 packagemanifests by passing manifests by dir:
  $ operator-sdk generate packagemanifests --deploy-dir deploy --version 0.0.1
  Generating package manifests version 0.0.1
  ...

  # After running in either of the above modes, you should see this directory structure:
  $ tree packagemanifests/
  packagemanifests/
  ├── 0.0.1
  │   ├── cache.my.domain_memcacheds.yaml
  │   └── memcached-operator.clusterserviceversion.yaml
  └── memcached-operator.package.yaml
`
)

// defaultRootDir is the default root directory in which to generate package manifests files.
const defaultRootDir = "packagemanifests"

// setDefaults sets command defaults.
func (c *packagemanifestsCmd) setDefaults() (err error) {
	if c.packageName, c.layout, err = genutil.GetPackageNameAndLayout(c.packageName); err != nil {
		return err
	}

	if !c.stdout && c.outputDir == "" {
		c.outputDir = defaultRootDir
	}

	c.generator = genpkg.NewGenerator()

	return nil
}

// validate validates c for package manifests generation.
func (c *packagemanifestsCmd) validate() error {
	var (
		err error
		sv  *semver.Version
	)
	if c.version != "" {
		if sv, err = genutil.ParseVersion(c.version); err != nil {
			return err
		}
		c.version = sv.String()
	} else {
		return errors.New("--version must be set")
	}

	if c.fromVersion != "" {
		if sv, err = genutil.ParseVersion(c.fromVersion); err != nil {
			return err
		}
		c.fromVersion = sv.String()
	}

	if c.inputDir == "" {
		return errors.New("--input-dir must be set")
	}

	if !genutil.IsPipeReader() {
		if c.deployDir == "" {
			return errors.New("--deploy-dir must be set if not reading from stdin")
		}
		if c.crdsDir == "" {
			return errors.New("--crds-dir must be set if not reading from stdin")
		}
	}

	if c.stdout {
		if c.outputDir != "" {
			return errors.New("--output-dir cannot be set if writing to stdout")
		}
	}

	if c.isDefaultChannel && c.channelName == "" {
		return fmt.Errorf("--default-channel can only be set if --channel is set")
	}

	return nil
}

// run generates package manifests.
func (c packagemanifestsCmd) run() error {

	c.println("Generating package manifests version", c.version)

	if err := c.generatePackageManifest(); err != nil {
		return err
	}

	col := &collector.Manifests{}
	if genutil.IsPipeReader() {
		if err := col.UpdateFromReader(os.Stdin); err != nil {
			return err
		}
	}
	if c.deployDir != "" {
		if err := col.UpdateFromDirs(c.deployDir, c.crdsDir); err != nil {
			return err
		}
	}

	// If no CSV was initially read, a kustomize base can be used at the default base path.
	// Only read from kustomizeDir if a base exists so users can still generate a barebones CSV.
	baseCSVPath := filepath.Join(c.kustomizeDir, "bases", c.packageName+".clusterserviceversion.yaml")
	if noCSVStdin := len(col.ClusterServiceVersions) == 0; noCSVStdin && genutil.IsExist(baseCSVPath) {
		base, err := bases.ClusterServiceVersion{BasePath: baseCSVPath}.GetBase()
		if err != nil {
			return fmt.Errorf("error reading CSV base: %v", err)
		}
		col.ClusterServiceVersions = append(col.ClusterServiceVersions, *base)
	} else if noCSVStdin {
		c.println("Building a ClusterServiceVersion without an existing base")
	}

	var opts []gencsv.Option
	stdout := genutil.NewMultiManifestWriter(os.Stdout)
	if c.stdout {
		opts = append(opts, gencsv.WithWriter(stdout))
	} else {
		opts = append(opts, gencsv.WithPackageWriter(c.outputDir))
	}

	csvGen := gencsv.Generator{
		OperatorName: c.packageName,
		Version:      c.version,
		FromVersion:  c.fromVersion,
		Collector:    col,
		Annotations:  metricsannotations.MakeBundleObjectAnnotations(c.layout),
	}
	if err := csvGen.Generate(opts...); err != nil {
		return fmt.Errorf("error generating ClusterServiceVersion: %v", err)
	}

	if c.updateObjects {
		// Extra ServiceAccounts not supported by this command.
		objs := genutil.GetManifestObjects(col, nil)
		if c.stdout {
			if err := genutil.WriteObjects(stdout, objs...); err != nil {
				return err
			}
		} else {
			dir := filepath.Join(c.outputDir, c.version)
			if err := genutil.WriteObjectsToFiles(dir, objs...); err != nil {
				return err
			}
		}
	}

	c.println("Package manifests generated successfully in", c.outputDir)

	return nil
}

func (c packagemanifestsCmd) generatePackageManifest() error {
	// copy of genpkg withfilewriter()
	// move out of internal util pkg?
	if err := os.MkdirAll(c.outputDir, 0755); err != nil {
		return err
	}

	opts := genpkg.Options{
		BaseDir:          c.inputDir,
		ChannelName:      c.channelName,
		IsDefaultChannel: c.isDefaultChannel,
	}

	if err := c.generator.Generate(c.packageName, c.version, c.outputDir, opts); err != nil {
		return err
	}
	return nil
}
