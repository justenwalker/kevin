---
title: "Trust the kevin CA"
description: "Install the kevin root CA so browsers and tools accept certificates from the kevin proxy."
weight: 2
---

# Trust the kevin CA

The kevin proxy serves HTTPS with certificates signed by a local CA. Until your machine trusts that CA, clients reject the certificates, and `curl` needs `--cacert ~/.kevin/root.crt`.

## Install the root CA

```sh
kevin ca install
```

Do this once per machine. It does not need a project, and it covers every kevin project.

kevin adds the root CA to:

- The macOS keychain, or the Linux CA directory, for your user.
- The certificate database of each Firefox profile.

macOS asks you to confirm the change.

To install for all users of the machine, add `--system`. This needs root: kevin prints the command to run with `sudo`.

## Trust the CA in Firefox

Firefox has its own certificate database. kevin needs `certutil` to change it, and skips Firefox if `certutil` is not installed. Install it, then run `kevin ca install` again:

```sh
brew install nss                  # macOS
sudo apt install libnss3-tools    # Debian and Ubuntu
sudo dnf install nss-tools        # Fedora
```

On macOS, Homebrew puts `certutil` in `$(brew --prefix nss)/bin`. Add that directory to your `PATH`.

## Check the result

```sh
kevin doctor
```

Each trust store shows `ok` when it trusts the kevin CA.

## Remove the root CA

```sh
kevin ca uninstall
```

## Related

- [Certificate authority]({{< relref "/docs/concepts/ca" >}}): how kevin creates and uses its CA.
- [`kevin ca`]({{< relref "/docs/reference/commands/ca" >}})
