---
title: "Intercept a hostname"
description: "Send traffic for a real hostname, such as an AWS endpoint, to a local container."
weight: 5
---

# Intercept a hostname

This guide sends traffic for a real hostname to a local service, with no change to the code that calls it. The example replaces Amazon S3 with [MiniStack](https://ministack.org/), a local AWS emulator.

## Start the local service

```cue
env: {
    fake_s3: {
        uses: "builtin:container"
        with: {image: "ministackorg/ministack", expose: s3: {port: 4566}}
    }
    fake_s3_ready: {
        uses:  "builtin:wait"
        needs: ["fake_s3"]
        with: {
            timeout: "15s"
            http: url: "http://${needs.fake_s3.out.host_4566}/"
        }
    }
}
```

The `wait` step makes the route start only after the service answers HTTP requests.

## Add intercept routes

```cue
s3_intercept: {
    uses:  "builtin:route"
    needs: ["fake_s3", "fake_s3_ready"]
    with: routes: [
        {host: "s3.us-east-1.amazonaws.com", address: "${needs.fake_s3.out.host_4566}", intercept: true},
        {host: "*.s3.us-east-1.amazonaws.com", address: "${needs.fake_s3.out.host_4566}", intercept: true},
    ]
}
```

With `intercept: true`, `host` is the full hostname, not a name on the environment domain. S3 clients use both the regional host and a subdomain for each bucket, so the example routes both.

If clients connect on a port other than 443, list the ports in `ports`.

## Send requests

From the host, through the proxy:

```sh
curl --proxy http://127.0.0.1:18080 https://s3.us-east-1.amazonaws.com/
```

From a container step, no proxy setting is needed: kevin sends the container's traffic to the proxy. Put the route step in the container's `needs`:

```cue
probe: {
    uses:  "builtin:container"
    needs: ["s3_intercept"]
    with: {
        image: "amazon/aws-cli"
        env: {
            AWS_ACCESS_KEY_ID:     "test"
            AWS_SECRET_ACCESS_KEY: "test"
            AWS_DEFAULT_REGION:    "us-east-1"
            AWS_CA_BUNDLE:         "/usr/local/share/ca-certificates/kevin.crt"
        }
        cmd: ["s3", "ls"]
    }
}
```

`AWS_CA_BUNDLE` points the AWS CLI at the kevin CA, which every container step has at `/usr/local/share/ca-certificates/kevin.crt`. Most tools use `SSL_CERT_FILE`, which kevin sets for you.

From a pod in a [`builtin:kind`]({{< relref "/docs/reference/steps/kind" >}}) cluster, no setting is needed. Use a Service address as the route `address` and set `relay` on the route step. See [Kubernetes clusters]({{< relref "kubernetes#give-a-service-a-name-on-the-environment-domain" >}}).

## Limits

- kevin sends traffic to the proxy for ports 80 and 443, and for the `ports` of intercept routes. Traffic on other ports goes directly to the internet.
- UDP and QUIC traffic is not intercepted. A client that uses HTTP/3 connects directly.

## Related

- [`examples/intercept`](https://github.com/justenwalker/kevin/tree/main/examples/intercept): this example, with a probe that uses the AWS CLI to create a bucket and upload a file.
- [Relay]({{< relref "/docs/concepts/relay" >}}): how kevin sends container and pod traffic to the proxy.
