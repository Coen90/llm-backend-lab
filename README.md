# First Agent

Java와 Go 기반 AI 백엔드를 단계적으로 개발하는 모노레포입니다.
모든 서비스는 이 Git 저장소에서 관리하며, Go 서비스는 각각 독립된 Go 모듈을 사용합니다.

## 구조

```text
services/
└── agent/
    ├── cmd/agent/main.go       # API 키 설정, Gin 서버 시작, /chat 등록
    ├── internal/chat/         # POST /chat, 입력 검증, HTTP 오류 응답
    ├── internal/llm/          # OpenAI Responses API 호출·응답 해석
    ├── .env.example
    └── go.mod
```

`services/agent`는 LLM 호출부터 시작해 Agent Runtime으로 발전시킬 첫 Go 서비스입니다.
현재는 질문 하나를 받아 모델의 답변 전체를 반환하는 동기 HTTP API입니다.
HTTP 서버는 Gin을 사용하고, OpenAI 호출은 Go 표준 라이브러리로 직접 구현합니다.

```text
Client → Gin POST /chat → Chat Handler → LLM Client → OpenAI /v1/responses
       ← {"answer":"..."}         ← 모델 응답
```

## 모델과 비용

기본 모델은 `gpt-5-nano`입니다. 2026-09-10 확인 기준 표준 요금은
100만 토큰당 입력 $0.05, 캐시된 입력 $0.005, 출력 $0.40입니다.
확인한 텍스트 모델 중 낮은 토큰 단가를 우선해 선택했습니다.

- 추론 강도는 `minimal`, 출력 한도는 요청당 2,048토큰입니다.
- 출력 한도와 출력 과금에는 눈에 보이는 답변뿐 아니라 추론 토큰도 포함됩니다.
- 단가가 낮더라도 질문과 추론량에 따라 요청당 실제 비용은 달라집니다.
- 한도에 도달해 답변이 미완성이면 HTTP 502 오류를 반환합니다.
  이 경우에도 사용된 토큰은 과금될 수 있습니다.
- 자동 재시도는 하지 않습니다. 요청 하나당 모델 호출은 한 번입니다.

공식 문서:
[GPT-5 nano 모델·가격](https://developers.openai.com/api/docs/models/gpt-5-nano),
[텍스트 생성과 응답 구조](https://developers.openai.com/api/docs/guides/text),
[추론 토큰과 출력 제한](https://developers.openai.com/api/docs/guides/reasoning).

## 실행

Go 1.26.3 이상이 필요합니다. 저장소 루트에서 실행합니다.

```sh
cd services/agent
cp .env.example .env
```

`.env`의 `OPENAI_API_KEY`에 자신의 OpenAI API 키를 입력합니다.
`.env`는 Git에서 제외됩니다. 프로그램은 `.env`를 자동으로 읽지 않으므로
같은 터미널에서 다음과 같이 환경변수로 불러온 뒤 실행합니다.

```sh
set -a
source ./.env
set +a
go run ./cmd/agent
```

| 환경변수 | 필수 | 기본값 | 설명 |
|---|---|---|---|
| `OPENAI_API_KEY` | 예 | 없음 | OpenAI API 키. 없으면 서버 시작 실패 |
| `HTTP_ADDR` | 아니요 | `127.0.0.1:8080` | 서버가 요청을 받는 주소 |

인증 기능이 없는 로컬 학습용 API이므로 기본적으로 로컬 인터페이스에서 실행합니다.

다른 터미널에서 호출합니다.

```sh
curl --fail-with-body http://127.0.0.1:8080/chat \
  -H 'Content-Type: application/json' \
  -d '{"message":"Go의 context 역할을 두 문장으로 설명해줘."}'
```

응답 예시(실제 문장은 모델 생성 결과에 따라 달라집니다):

```json
{"answer":"Go의 context는 작업의 취소 신호와 시간 제한을 전달합니다. 요청이 취소되면 하위 작업도 이를 감지하고 중단할 수 있습니다."}
```

Gin의 기본 접근 로그와 모델 호출 실패 로그를 사용합니다.
토큰 사용량은 LLM Client의 반환값에 포함되며, 별도 집계는 다음 단계에서 추가합니다.

## 요청과 오류 처리

- JSON 요청에 비어 있지 않은 문자열 `message`가 필요합니다. 공백만 있는 질문도 거절합니다.
- `ShouldBindJSON`으로 요청을 읽고 `binding:"required"`로 필수 필드를 검증합니다.
  JSON 읽기는 16 KiB로 제한합니다. 추가 필드와 뒤따르는 JSON 값의 엄격한 검증은 하지 않습니다.
- LLM 호출 제한 시간은 30초입니다. 클라이언트 요청이 취소되면 모델 호출에도 취소가 전달됩니다.
- 대화 이력, 스트리밍, Tool Calling은 아직 없으며 매 요청을 독립적으로 처리합니다.

| HTTP 상태 | 의미 |
|---|---|
| 400 | JSON 형식, 질문 또는 JSON 읽기 크기 오류 |
| 502 | 모델 호출 실패 또는 미완성 답변 |
| 504 | 모델 호출 시간 초과 |

```json
{"error":"message must not be empty"}
```

없는 경로와 허용되지 않은 메서드는 Gin의 기본 404/405 응답을 반환합니다.

이번 단계에서는 별도 종료 제어, 상세 오류 분류, 토큰 집계 코드를 두지 않습니다.
LLM Client에 30초 호출 제한을 두고, Gin 요청의 Context를 전달해 취소를 처리합니다.

## 검증

`services/agent`에서 실행합니다.

```sh
go build ./...
go vet ./...
go test -race ./...
```

테스트는 모의 HTTP 전송과 모델 응답을 사용하므로 실제 API 키나 과금이 필요하지 않습니다.
요청 형식, 여러 응답 항목에서 텍스트 추출, 입력 검증, 미완성 응답,
호출 오류, 시간 초과, 취소 전파를 검증합니다.

아직 원격 저장소 주소가 정해지지 않아 모듈 경로는
`example.com/first-agent/services/agent`를 사용합니다.
원격 저장소가 정해지면 실제 경로로 변경할 수 있습니다.
