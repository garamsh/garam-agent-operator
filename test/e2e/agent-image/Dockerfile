# The image the e2e suite's Agent runs in place of sherlock's agent, which the
# suite cannot run. The suite builds it and serves it to the cluster itself.
FROM busybox:1.37@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e

COPY entrypoint.sh /usr/local/bin/agent-stand-in

# The user every container of an agent's Pod is given.
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/agent-stand-in"]
