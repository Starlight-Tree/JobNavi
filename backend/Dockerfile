# JobNavi の API（Go）のイメージ。標準ライブラリと少数の依存だけなので、CGo なしで静的にビルドして distroless に置く（シェルも curl も無い）。
# ビルドの文脈は backend/（docker-compose.yml・e2e/compose.yml が指す）。依存は go.sum で固定し、go mod verify で内容を確かめてからビルドする。
# `-tags nodynamic`: WebP の符号化・復号は、共有ライブラリを探さず、組み込みの（Go に変換した）libwebp だけを使う。
FROM golang:1.26.10 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY *.go demo_images.json ./
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -tags nodynamic -trimpath -ldflags="-s -w" -o /api . \
    && mkdir -p /data/media

FROM gcr.io/distroless/static-debian12
COPY --from=build /api /api
# 開発用の画像の保存先（名前付きボリュームを重ねたとき、uid 10001 が書けるように所有者を合わせる。旧 Python のイメージと同じ uid）
COPY --from=build --chown=10001:10001 /data /data
USER 10001:10001
EXPOSE 8000
# 引数なしは API の起動。道具は `/api migrate`・`/api seed`・`/api seed-images`・`/api images …`・`/api search-index`（docker-compose.yml の tools）
ENTRYPOINT ["/api"]
