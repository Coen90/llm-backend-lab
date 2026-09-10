# LLM Backend Lab

Java·Go 기반 AI 백엔드를 단계적으로 만드는 학습용 모노레포입니다.
LLM 요청·응답을 직접 이해하고, 이후 Agent Runtime으로 발전시키는 것이 목표입니다.

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
event: delta
data: {"text":"안녕"}

event: delta
data: {"text":"하세요!"}

event: done
data: {"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}

```

- `delta`: 새로 도착한 텍스트입니다. 공백·줄바꿈을 포함해 순서대로 이어 붙입니다. 모델의 거절 안내도 텍스트로 전달합니다.
- `done`: 모델이 정상 완료했음을 뜻합니다. 최종 토큰 사용량을 포함하며, 이미 보낸 텍스트는 다시 보내지 않습니다.
- `error`: 일부 응답을 보낸 뒤 실패한 경우입니다. 예: `{"code":"incomplete_response","message":"model response is incomplete"}`. 이 경우 `done`은 보내지 않습니다.

첫 `delta`를 보내기 전에는 잘못된 입력을 HTTP 400, 모델 호출 실패를 502, 시간 초과를 504와 JSON 오류로 반환합니다.
전송 시작 후에는 HTTP 상태가 200으로 유지되므로 `error` 이벤트를 확인해야 합니다.
연결이 닫혔다는 사실만으로 성공으로 판단하지 않고, 반드시 `done`을 확인합니다.

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

브라우저에서 POST 본문을 보내고 스트림을 읽을 때는 `fetch` 등을 사용합니다.

검증은 `services/agent`에서 `go test ./...`로 실행합니다. 테스트는 실제 API를 호출하지 않습니다.
로컬 HTTP 서버로 완료 전 첫 이벤트 전달, 중간 오류, 연결 취소를 검증하고,
SSE 파싱에서는 나뉘어 도착하는 UTF-8·CRLF·여러 `data` 줄·미완성 종료를 확인합니다.

참고: [OpenAI 텍스트 생성](https://developers.openai.com/api/docs/guides/text) · [OpenAI 스트리밍](https://developers.openai.com/api/docs/guides/streaming-responses) · [Gin 시작 가이드](https://gin-gonic.com/en/docs/quickstart/)
