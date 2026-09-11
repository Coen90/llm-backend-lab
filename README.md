# LLM Backend Lab

Java·Go 기반 AI 백엔드를 단계적으로 만드는 학습용 모노레포입니다.
LLM 요청·응답을 직접 이해하고, 이후 Agent Runtime으로 발전시키는 것이 목표입니다.

개발 과정과 선택의 이유는 다음 글에 정리했습니다.

- [Stage 01 — 질문에 답하는 API 만들기](docs/blog/stage-01-llm-serving.md)
- [Stage 02 — 답변 스트리밍과 생성 중단](docs/blog/stage-02-llm-streaming.md)

## Stage 01 — LLM Serving

`POST /chat`으로 질문을 받아 OpenAI `gpt-5-nano`를 호출하고, 완성된 답변을 반환합니다.
기본 입력 검증, 30초 호출 제한, 요청 취소와 오류 처리를 포함합니다.

```text
사용자 {"message":"질문"} → Gin Handler → LLM Client → OpenAI
사용자 {"answer":"답변"} ← Gin Handler ← LLM Client ← 모델 응답
```

현재는 대화 이력 없이 요청을 독립적으로 처리하며, 자동 재시도는 하지 않습니다.

## Stage 02 — LLM Streaming

`POST /chat/stream`은 같은 `{"message":"질문"}`을 받고, 생성된 내용을 SSE로 전달합니다.
기존 `POST /chat`은 완성된 JSON 답변을 반환하는 방식으로 유지합니다.

```text
OpenAI SSE → LLM Client: 이벤트 읽기 → Gin Handler: 전송·Flush → 사용자
```

우리 API의 이벤트 형식은 다음과 같습니다. `data`는 JSON이며 아래 텍스트와 사용량은 예시입니다.

```text
event: started
data: {"stream_id":"abc123"}

event: delta
data: {"text":"안녕"}

event: delta
data: {"text":"하세요!"}

event: done
data: {"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}

```

- `started`: 해당 작업의 `stream_id`입니다. 모델 호출 전에 전송하므로 첫 텍스트를 기다리는 동안에도 중단할 수 있습니다.
- `delta`: 새로 도착한 텍스트입니다. 공백·줄바꿈을 포함해 순서대로 이어 붙입니다. 모델의 거절 안내도 텍스트로 전달합니다.
- `done`: 모델이 정상 완료했음을 뜻합니다. 최종 토큰 사용량을 포함하며, 이미 보낸 텍스트는 다시 보내지 않습니다.
- `user_cancelled`: 중단 API로 모델 호출을 취소한 경우입니다. 마지막 안내를 보낸 뒤 SSE 응답을 끝내며 `done`은 보내지 않습니다.
- `error`: 모델 호출이 실패한 경우입니다. 예: `{"code":"incomplete_response","message":"model response is incomplete"}`. 이 경우 `done`은 보내지 않습니다.

잘못된 입력은 스트림을 시작하지 않고 HTTP 400과 JSON 오류로 반환합니다.
유효한 입력에는 먼저 HTTP 200으로 `started`를 보냅니다. 이후 모델 호출 실패와 시간 초과도 `error` 이벤트로 전달합니다.
종료 이벤트는 `done`, `error`, `user_cancelled` 중 하나입니다. 연결이 닫혔다는 사실만으로 성공으로 판단하지 않습니다.

사용자가 연결을 끊거나 쓰기에 실패하면 외부 요청을 취소하고 응답 본문을 닫습니다.
모델 호출은 스트림 전체를 포함해 최대 30초이며, SSE 이벤트 하나는 최대 1 MiB로 제한합니다.
클라이언트로 보내는 쓰기에는 별도 시간 제한을 두지 않습니다.
자동 재접속과 재시도는 하지 않습니다.

구현은 [LLM 스트림 처리](services/agent/internal/llm/stream.go)와
[스트리밍 Handler](services/agent/internal/chat/stream.go)에서 확인할 수 있습니다.
Client는 이벤트를 읽어 콜백으로 텍스트를 넘기고, Handler는 Gin의 `SSEvent`와 `Flush`로 즉시 전달합니다.
별도의 goroutine이나 큐를 만들지 않습니다.

## 기술 선택과 고민

| 선택 | 이유와 고려사항 |
|---|---|
| Go | 동시 실행과 취소를 다루는 Agent Runtime 개발을 배우기 위해 선택했습니다. Java 대비 성능 우위는 가정하지 않고 이후 측정합니다. |
| Gin | 서버의 반복 코드를 줄여 LLM 통신 학습에 집중합니다. 프레임워크 의존성을 추가하는 비용을 받아들였습니다. |
| 직접 HTTP 호출 | SDK 대신 요청·응답 규격과 오류 처리를 직접 익힙니다. 기능이 늘어 유지보수 부담이 커지면 SDK 전환을 검토합니다. |
| 저렴한 모델 | 반복 실험 비용을 낮춥니다. 향후 대표 질문으로 품질·응답 시간·사용량을 비교해 모델 변경을 판단합니다. |
| Handler / Client 분리 | HTTP 처리와 모델 통신의 변경 범위를 나누고, 모의 Client로 테스트하기 위해 분리했습니다. |
| 모노레포 / 서비스별 모듈 | 개발 이력은 함께 관리하고 의존성은 서비스별로 관리합니다. 공통 모듈은 실제 중복과 필요가 생길 때 검토합니다. |

스트리밍에서는 답변이 완성되기 전에 생성된 부분부터 전달합니다.
대화 상태를 추가할 때는 원본 기록 보관과 모델에 보낼 Context를 구분하고,
토큰 예산에 따라 최근 대화와 요약을 함께 사용하는 방식을 검토합니다.

## 실행

Go 1.26.3 이상이 필요합니다. `services/agent/.env.example`을 복사해 `.env`를 만들고
`OPENAI_API_KEY`를 입력합니다. `.env`는 Git에서 제외되며 자동으로 로드되지 않습니다.

```sh
cd services/agent
set -a
source ./.env
set +a
go run ./cmd/agent
```

다른 터미널에서 호출합니다. 기본 주소는 `127.0.0.1:8080`입니다.

```sh
curl -i http://127.0.0.1:8080/chat \
  -H 'Content-Type: application/json' \
  -d '{"message":"Go의 context 역할을 두 문장으로 설명해줘."}'
```

스트리밍을 확인할 때는 `-N`으로 curl의 출력 버퍼링을 끕니다.

```sh
curl -N -i http://127.0.0.1:8080/chat/stream \
  -H 'Content-Type: application/json' \
  -d '{"message":"Go의 context 역할을 예시와 함께 설명해줘."}'
```

### 생성 중단

스트리밍을 받는 터미널은 열어둔 채 다른 터미널에서 중단 요청을 보냅니다.
`STREAM_ID`는 첫 `started` 이벤트에서 받은 값으로 바꿉니다.

```sh
curl -i -X POST http://127.0.0.1:8080/chat/streams/STREAM_ID/stop
```

중단 요청은 HTTP 202와 `{"status":"stop_requested"}`를 반환합니다.
원래 스트리밍 연결에서는 다음 이벤트를 받은 뒤 응답이 끝납니다.

```text
event: user_cancelled
data: {"message":"사용자가 답변 생성을 중단했습니다."}

```

처리 중인 작업에 대한 반복 중단 요청도 202로 응답합니다. 이미 종료됐거나 존재하지 않는 ID는 404입니다.
완료 처리보다 중단 요청이 먼저 받아들여졌다면 `user_cancelled`, 완료 처리가 먼저 끝났다면 기존 완료 결과를 유지합니다.
작업은 완료·실패·사용자 연결 종료 시 메모리에서 제거합니다.

브라우저에서는 `fetch`로 SSE를 읽고, 중단 버튼은 별도 요청을 보냅니다.

```js
await fetch(`/chat/streams/${streamId}/stop`, { method: "POST" });
```

생성 중단 버튼에서 원래 스트리밍 요청을 `abort()`하면 마지막 이벤트를 받을 수 없습니다.
사용자 연결용 Context는 유지하고 자식인 모델 호출용 Context만 취소하도록 구현했습니다.
브라우저를 닫아 연결 자체가 끊어지면 부모와 자식 Context가 함께 취소되며 SSE 안내는 보내지 않습니다.

작업 보관소는 [StreamRegistry](services/agent/internal/chat/stream_registry.go)의 메모리 map이며 단일 서버 프로세스용입니다.
현재는 로그인 없이 발급받은 무작위 ID로 중단하므로 ID를 가진 쪽이 해당 작업을 중단할 수 있습니다.
인증을 추가할 때 작업 소유자 확인도 함께 적용해야 합니다.

검증은 `services/agent`에서 `go test ./...`로 실행합니다. 테스트는 실제 API를 호출하지 않습니다.
로컬 HTTP 서버로 완료 전 첫 이벤트 전달, 중간 오류, 연결 취소, 첫 텍스트 전·후 생성 중단을 검증합니다.
반복 중단, 완료와 중단의 동시 처리, 다른 작업과의 취소 격리도 확인하며,
SSE 파싱에서는 나뉘어 도착하는 UTF-8·CRLF·여러 `data` 줄·미완성 종료를 확인합니다.

참고: [OpenAI 텍스트 생성](https://developers.openai.com/api/docs/guides/text) · [OpenAI 스트리밍](https://developers.openai.com/api/docs/guides/streaming-responses) · [Gin 시작 가이드](https://gin-gonic.com/en/docs/quickstart/)
