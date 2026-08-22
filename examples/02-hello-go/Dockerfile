# Multi-stage build: compile a static binary, then ship it on the AWS-provided
# custom-runtime base. aws-lambda-go's lambda.Start speaks the Runtime API
# itself, so the provided base needs nothing but the binary.
FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
# lambda.norpc drops the unused net/rpc code path, shrinking the binary.
RUN CGO_ENABLED=0 GOOS=linux go build -tags lambda.norpc -o /main .

FROM public.ecr.aws/lambda/provided:al2023
COPY --from=build /main ./main
ENTRYPOINT ["./main"]
