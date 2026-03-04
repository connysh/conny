FROM gcr.io/distroless/static-debian12:nonroot

ARG TARGETPLATFORM

COPY ${TARGETPLATFORM}/conny /conny

ENTRYPOINT ["/conny"]
