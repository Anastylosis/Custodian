# Template for the Homebrew formula. The release workflow renders this into
# Anastylosis/homebrew-tap as Formula/custodian.rb, substituting VERSION and
# the four __SHA256_*__ placeholders with the checksums from the release's
# SHA256SUMS.
#
# Binary, not source: the release already publishes darwin and linux
# tarballs, so an install is a download and an extract. A source formula
# would make every user build with a Go toolchain they may not have.
#
# Keep this file and the workflow's placeholder list in step — the render step
# fails loudly on a leftover placeholder rather than shipping a formula that
# cannot compute a checksum.
class Custodian < Formula
  desc "Finds and resolves duplicate video scenes in a Stash library"
  homepage "https://github.com/Anastylosis/Custodian"
  # Explicit on purpose. Homebrew's URL scan reads
  # custodian-v0.4.1-darwin-arm64.tar.gz as version "64" on macOS (it takes
  # the trailing number), so without this line `brew test` compares against
  # 64. On Linux the amd64 URL scans correctly, which makes `brew audit` call
  # the line redundant there — the tap's CI runs audit with --except=version
  # for exactly this reason.
  version "__VERSION__"
  license "GPL-3.0-only"

  on_macos do
    on_arm do
      url "https://github.com/Anastylosis/Custodian/releases/download/v__VERSION__/custodian-v__VERSION__-darwin-arm64.tar.gz"
      sha256 "__SHA256_DARWIN_ARM64__"
    end
    on_intel do
      url "https://github.com/Anastylosis/Custodian/releases/download/v__VERSION__/custodian-v__VERSION__-darwin-amd64.tar.gz"
      sha256 "__SHA256_DARWIN_AMD64__"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/Anastylosis/Custodian/releases/download/v__VERSION__/custodian-v__VERSION__-linux-arm64.tar.gz"
      sha256 "__SHA256_LINUX_ARM64__"
    end
    on_intel do
      url "https://github.com/Anastylosis/Custodian/releases/download/v__VERSION__/custodian-v__VERSION__-linux-amd64.tar.gz"
      sha256 "__SHA256_LINUX_AMD64__"
    end
  end

  def install
    bin.install "custodian"
    generate_completions_from_executable(bin/"custodian", "completion")
  end

  test do
    # `custodian config init` and `--version` never touch the network, unlike
    # a command that reaches Stash — see internal/cli/config.go. A formula
    # test that needs the network fails in Homebrew's sandboxed CI for
    # reasons unrelated to the package.
    assert_match "Stash", shell_output("#{bin}/custodian --help")
    assert_match version.to_s, shell_output("#{bin}/custodian --version")

    system bin/"custodian", "config", "init", "--config", testpath/"config.yaml"
    assert_predicate testpath/"config.yaml", :exist?
  end
end
