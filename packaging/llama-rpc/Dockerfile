# llama.cpp with its RPC backend switched on, so one model can be split
# across machines. The official images are built without RPC.
#
#   worker:  ggml-rpc-server -H 0.0.0.0 -p 50052 -c
#   head:    llama-server -hf <repo> --rpc host1:50052,host2:50052 -ngl 99
#
# Built and tested by .github/workflows/llama-rpc.yml.
FROM ubuntu:24.04 AS build
ARG LLAMA_VERSION=b12019
RUN apt-get update \
 && apt-get install -y --no-install-recommends build-essential cmake git ca-certificates libssl-dev \
 && rm -rf /var/lib/apt/lists/*
RUN git clone --depth 1 --branch "${LLAMA_VERSION}" https://github.com/ggml-org/llama.cpp /src
WORKDIR /src
RUN cmake -B build -DCMAKE_BUILD_TYPE=Release -DGGML_RPC=ON -DGGML_NATIVE=OFF -DBUILD_SHARED_LIBS=OFF \
      -DLLAMA_BUILD_TESTS=OFF -DLLAMA_BUILD_EXAMPLES=OFF \
 && cmake --build build -j"$(nproc)" --target llama-server ggml-rpc-server

FROM ubuntu:24.04
ARG LLAMA_VERSION=b12019
RUN apt-get update \
 && apt-get install -y --no-install-recommends libgomp1 libssl3 ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /src/build/bin/llama-server /src/build/bin/ggml-rpc-server /usr/local/bin/
LABEL org.opencontainers.image.title="llama.cpp with RPC" \
      org.opencontainers.image.description="llama-server and ggml-rpc-server for splitting a model across machines with sys1" \
      org.opencontainers.image.source="https://github.com/A-Hackers-Guide/sys1" \
      org.opencontainers.image.version="${LLAMA_VERSION}" \
      org.opencontainers.image.licenses="MIT"
WORKDIR /root
