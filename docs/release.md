# Release
This document describes how to release the Cluster API BYOH infrastructure provider.

A tag `vX.Y.Z` starts the [Draft Release](https://github.com/superbrothers/cluster-api-provider-bringyourownhost/actions/workflows/draft-release.yaml) workflow, which publishes the release in two jobs:

- `image` builds the controller image for amd64 and arm64, and pushes it as `ghcr.io/superbrothers/cluster-api-byoh-controller:vX.Y.Z`.
- `release` runs after the image is pushed. It builds the following assets and creates a draft release of the tag with them:
  - `infrastructure-components.yaml`, which uses the image of the tag, and `metadata.yaml`
  - the cluster templates
  - the agent for amd64 and arm64: the deb packages `byoh-hostagent_<version>_<arch>.deb` and the binaries `byoh-hostagent-linux-<arch>`
  - `checksums.txt`, the SHA-256 checksums of the other assets, which `sha256sum --check` reads

The draft is hidden until it is published, so the users and clusterctl get the release only after that. Only clusterctl with a `GITHUB_TOKEN` that can push to this repository lists the draft too.

We follow semantic versioning for the releases, see https://semver.org.

---
### Steps

1. If a new major or minor version is being released, open a PR that adds its release series to `metadata.yaml`, mapped to the Cluster API contract that the provider implements. This is not needed for a patch release. The provider implements the `v1beta1` contract. clusterctl v1.14 installs providers of the `v1beta1` contract along with a core provider of the `v1beta2` contract, until `v1beta1` reaches its end of life.

2. Tag the commit of `main` to release, and push the tag:
   ```shell
   git checkout main
   git pull
   git tag -a v0.6.0 -m v0.6.0
   git push origin v0.6.0
   ```

3. Wait for the [Draft Release](https://github.com/superbrothers/cluster-api-provider-bringyourownhost/actions/workflows/draft-release.yaml) workflow to finish. If a job fails without a change of the code, re-run the failed jobs. If the fix needs a change of the code, delete the draft release and the tag, merge the fix and tag again. `gh release delete --cleanup-tag` deletes the release and the tag in the repository:
   ```shell
   gh release delete v0.6.0 --cleanup-tag
   git tag -d v0.6.0
   ```

4. The first time only, check that the image can be pulled without credentials, as the kubelets pull it without a pull Secret. Open `https://github.com/superbrothers/cluster-api-provider-bringyourownhost/pkgs/container/cluster-api-byoh-controller` without signing in, or pull the image after logging out:
   ```shell
   docker logout ghcr.io
   docker pull ghcr.io/superbrothers/cluster-api-byoh-controller:v0.6.0
   ```
   `docker buildx imagetools inspect` uses the stored credentials, so it does not show whether the package is public. The package of this repository became public when the workflow pushed it for the first time, although GitHub documents private as the default. If it is private, open `https://github.com/users/superbrothers/packages/container/cluster-api-byoh-controller/settings`, and change the visibility to public in "Danger Zone". A public package cannot be made private again.

5. Check the draft release and the image:
   - The release has all the assets above, they match `checksums.txt`, and `infrastructure-components.yaml` uses the image of the tag:
     ```shell
     gh release download v0.6.0 -D v0.6.0
     cd v0.6.0
     sha256sum --check checksums.txt
     grep 'image: ghcr.io' infrastructure-components.yaml
     ```
   - The image has `linux/amd64` and `linux/arm64`:
     ```shell
     docker buildx imagetools inspect ghcr.io/superbrothers/cluster-api-byoh-controller:v0.6.0
     ```

6. Publish the draft release with "Publish release" in the GitHub UI, or with `gh release edit v0.6.0 --draft=false`.

7. Check that clusterctl installs the release into a disposable management cluster. The `byoh` provider built into clusterctl points to the upstream repository, so override it in `clusterctl.yaml` (by default `~/.config/cluster-api/clusterctl.yaml`):
   ```yaml
   providers:
   - name: byoh
     url: https://github.com/superbrothers/cluster-api-provider-bringyourownhost/releases/latest/infrastructure-components.yaml
     type: InfrastructureProvider
   ```
   ```shell
   clusterctl init --infrastructure byoh:v0.6.0
   ```

8. Update the documentation, if applicable.

### Pre-releases

To try the workflow, for example before the first release of a minor version, push a pre-release tag such as `v0.6.0-rc.0` first, and check it with the steps 3 to 5. The workflow marks the release of a tag with a `-` as a pre-release. When clusterctl picks the latest version, it prefers releases to pre-releases, but it picks a pre-release if the repository has no release. Delete the draft release and the tag if they are not needed, with `gh release delete v0.6.0-rc.0 --cleanup-tag` and `git tag -d v0.6.0-rc.0`, and delete the version of the image in the package, if needed.
