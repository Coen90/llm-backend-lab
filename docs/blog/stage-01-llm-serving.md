# 에이전트 백엔드: 01. 질문에 답하는 API 만들기

Java/Spring으로 백엔드를 개발해왔다. 이제는 그 경험을 바탕으로 LLM과 Agent를 활용하는 시스템까지 직접 설계하고 운영해보고 싶다. 장기적으로 일본 취업을 준비하면서 기존 경력에 어떤 개발 경험을 더할지도 함께 고민했다.

그래서 **LLM Backend Lab**을 시작했다. 최종 목표는 **여러 Agent가 공통으로 사용할 수 있는 실행 환경인 Agent Runtime과 LLM Gateway를 만드는 것**이다. Java는 업무 규칙과 트랜잭션을, Go는 Agent 실행과 동시성 제어를 담당하는 방향을 생각하고 있다. 작은 기능부터 만들며 선택의 이유와 구현 과정을 기록하려고 한다.

첫 작업은 질문을 받아 답변하는 API다. 이 글에서는 요청이 모델에 전달되고 답변으로 돌아오는 과정을 따라가며, 응답 파싱과 완료 상태 확인, 시간 제한과 취소를 어떻게 처리했는지 살펴본다.

## 1. 목표와 목적

이번 단계의 목표는 **사용자 질문을 받아 LLM에 전달하고, 완성된 답변을 반환하는 `POST /chat` API를 만드는 것**이다. 잘못된 입력을 걸러내고 모델 호출이 실패하거나 늦어졌을 때 응답하는 처리까지 구현한다.

목적은 **LLM 호출을 백엔드 서비스의 기능으로 제공하는 데 필요한 처리 과정을 익히는 것**이다. 사용자 요청을 모델의 요청 형식으로 바꾸고, 응답에서 답변을 추출하며, 완료 여부와 오류를 판단하는 과정을 직접 다뤄보려 한다. 여기서 익힌 시간 제한과 취소 처리는 이후 Agent Runtime에서 여러 모델과 도구의 실행을 제어하는 기반으로 삼을 계획이다.

구현 범위는 대화 이력 없이 질문 하나에 답변 하나를 돌려주는 흐름으로 정했다. 먼저 한 번의 요청을 입력부터 응답까지 처리하고, 스트리밍·대화 이력·도구 실행은 이후 단계에서 확장한다.

## 2. 질문 하나가 답변으로 돌아오기까지

Go와 Gin으로 `POST /chat` API를 만들었다. 모델은 `gpt-5-nano`를 사용하며 OpenAI Responses API를 HTTP로 직접 호출한다. 전체 코드는 [Stage 01 브랜치](https://github.com/Coen90/llm-backend-lab/tree/feature/stage-01-llm-serving)에 있다.

### 먼저 API를 호출해보기

저장소의 `services/agent`에서 `.env.example`을 `.env`로 복사하고 `OPENAI_API_KEY`를 입력한다. `.env`는 자동으로 읽히지 않으므로 환경변수로 불러온 뒤 실행한다. Go 설치 등 실행 준비는 [프로젝트 README](https://github.com/Coen90/llm-backend-lab/blob/feature/stage-01-llm-serving/README.md)를 참고하면 된다.

```sh
cd services/agent
set -a
source ./.env
set +a
go run ./cmd/agent
```

다른 터미널에서 질문을 보낸다.

```sh
curl -i http://127.0.0.1:8080/chat \
  -H 'Content-Type: application/json' \
  -d '{"message":"Go의 context는 어떤 역할을 해?"}'
```

성공하면 HTTP 200과 함께 `answer` 필드를 받는다. 아래는 응답 형식을 보여주는 예시이며 실제 답변 내용은 모델이 생성한다.

```json
{"answer":"모델이 생성한 답변"}
```

### Handler에서 LLM Client로

```text
POST /chat {"message":"질문"}
  → Gin Handler: JSON과 질문 검증
  → LLM Client: OpenAI 요청 생성·전송
  → OpenAI: 모델 응답
  → LLM Client: 완료 상태 확인·답변 추출
  → Gin Handler: {"answer":"답변"} 반환
```

코드는 이 흐름에 맞춰 나눴다. `services/agent` 아래에서 각 파일이 맡는 일은 다음과 같다.

| 파일 | 맡은 일 |
| --- | --- |
| `cmd/agent/main.go` | 환경변수를 읽고 Client와 Handler를 연결해 서버 실행 |
| `internal/chat/handler.go` | 사용자 입력 검증, 모델 호출, HTTP 응답 |
| `internal/llm/client.go` | OpenAI 요청 전송, 상태 확인, 답변 추출 |
| `internal/llm/types.go` | 외부 API 요청·응답과 내부 결과 타입 정의 |

Handler는 JSON을 읽고 `message`의 앞뒤 공백을 제거한다. 질문이 비어 있으면 모델을 호출하지 않고 HTTP 400을 반환한다. 입력 검증이 끝나면 Client에 질문과 요청의 Context를 전달한다.

```go
result, err := client.Reply(c.Request.Context(), request.Message)
```

### 사용자 질문을 모델 요청으로 바꾸기

우리 API의 `message`는 OpenAI 요청의 `input`으로 들어간다. 현재 Client가 보내는 JSON 본문은 아래와 같다.

```json
{
  "model": "gpt-5-nano",
  "input": "Go의 context는 어떤 역할을 해?",
  "store": false,
  "max_output_tokens": 2048,
  "reasoning": {
    "effort": "minimal"
  }
}
```

모델과 출력 한도는 서버에서 정하고, API 키도 서버의 환경변수에서 읽어 `Authorization` 헤더에 넣는다. 사용자는 질문만 전달하면 된다. 외부 API의 요청·응답 타입은 `types.go`에 따로 정의해 어떤 필드를 주고받는지 확인하기 쉽게 했다.

응답이 도착하면 Client가 상태와 내용을 검사한다. 성공한 결과만 Handler로 돌려주고, Handler는 `answer` 필드에 답변을 담는다. 사용자의 요청 형식, 모델과 통신하는 형식, 사용자에게 돌려줄 형식을 각각 구분한 구조다.

## 3. 답변을 반환하기 전에 확인할 것들

외부 API에 요청을 보낸 뒤에는 무엇을 성공으로 볼지 정해야 했다. 이번 API에서는 사용할 수 있는 답변이 완성됐는지 확인하고, 실패했을 때는 그 상황에 맞는 HTTP 응답을 보내기로 했다.

### 첫 번째 응답 항목이 답변이라고 가정하지 않기

Responses API의 `output`에는 메시지 외에도 추론 관련 항목이나 도구 호출이 들어갈 수 있다. 그래서 `output[0].content[0].text`처럼 고정된 위치에서 답변을 꺼내면 안 된다. [OpenAI 텍스트 생성 문서](https://developers.openai.com/api/docs/guides/text)에서도 이 점을 설명한다.

이 프로젝트의 테스트에는 추론 항목이 먼저 나오고, 그 뒤에 두 개의 메시지가 오는 응답을 넣었다. 아래는 그 구조를 간추린 예시다.

```text
output[0] → reasoning
output[1] → message → output_text: "안녕"
output[2] → message → output_text: "하세요!"
```

기대하는 결과는 두 텍스트를 합친 `안녕하세요!`다. 이를 위해 배열을 순회하면서 assistant 메시지를 고르고, 그 안의 내용도 타입별로 읽는다. 다음은 Client의 답변 추출 부분이다.

```go
var answer strings.Builder
for _, item := range response.Output {
    if item.Type != "message" || item.Role != "assistant" {
        continue
    }
    for _, content := range item.Content {
        switch content.Type {
        case "output_text":
            answer.WriteString(content.Text)
        case "refusal":
            answer.WriteString(content.Refusal)
        }
    }
}
```

모델이 요청을 거절한 경우에는 `refusal`의 안내문을 전달한다. 추출한 결과가 공백뿐이라면 유효한 답변으로 처리하지 않는다. 배열 위치보다 항목의 타입과 역할을 기준으로 읽도록 구현했다.

### HTTP 요청 성공과 답변 완료를 구분하기

출력 한도인 `max_output_tokens`에는 사용자에게 보이는 답변 외에 추론 토큰도 포함된다. 한도에 도달하면 응답의 `status`가 `incomplete`일 수 있다. 따라서 2048을 설정했다고 답변 텍스트에 그만큼을 모두 사용할 수 있는 것은 아니다. [OpenAI 추론 모델 문서](https://developers.openai.com/api/docs/guides/reasoning#allocating-space-for-reasoning)

Client는 HTTP 상태를 확인한 다음 응답 본문의 완료 상태도 검사한다. 실제 코드에서는 답변을 추출하기 전에 다음 검사를 거친다.

```go
if response.Status == "incomplete" {
    return result, ErrIncomplete
}
if response.Status != "completed" {
    return result, ErrInvalidResponse
}
```

이번에는 완성된 답변 하나를 반환하기로 했으므로, 일부 텍스트가 있어도 미완성이면 HTTP 502로 처리한다. 부분 답변을 보여주는 정책도 가능하지만 현재 API에는 적용하지 않았다. `completed` 역시 내용의 정확성을 보장하는 표시는 아니다. 모델 답변의 품질은 별도로 평가해야 한다.

### 기다리는 시간과 취소 경로 정하기

모델 호출은 응답이 올 때까지 서버 자원을 사용한다. 이 프로젝트에서는 HTTP Client에 30초 제한을 두었다.

```go
httpClient: &http.Client{Timeout: 30 * time.Second},
```

시간 제한과 별개로 사용자 요청의 취소도 모델 호출에 전달한다. Handler가 받은 `c.Request.Context()`를 Client의 `Reply`에 넘기고, Client는 그 Context로 외부 HTTP 요청을 만든다.

```go
req, err := http.NewRequestWithContext(
    ctx,
    http.MethodPost,
    "https://api.openai.com/v1/responses",
    bytes.NewReader(body),
)
```

여기서 별도의 `context.Background()`로 바꾸면 사용자 요청의 취소와 외부 호출이 분리된다. 전달받은 Context를 이어 사용해 두 요청의 취소 경로를 연결했다. 사용자 요청이 이미 취소됐다면 Handler는 오류 응답을 새로 쓰지 않고 종료한다. 이 코드는 우리 서버의 외부 HTTP 요청을 취소하는 범위까지 다룬다.

### 실패 응답과 테스트 기준 정하기

현재 구현의 오류 처리는 다음과 같다. 이는 이 프로젝트에서 정한 정책이다.

| 상황 | 우리 API의 처리 |
| --- | --- |
| 잘못된 JSON, 빈 질문, 16 KiB 본문 제한 초과 | HTTP 400, 모델 호출 안 함 |
| 모델 호출 시간 초과 | HTTP 504 |
| 제공자 오류, 네트워크 실패, 미완성·잘못된 모델 응답 | HTTP 502 |
| 사용자 요청 취소 | 외부 요청에 취소 전달, 응답 작성 중단 |

OpenAI가 반환한 401이나 429도 현재는 502로 묶는다. 세분화된 오류 정책과 자동 재시도는 아직 구현하지 않았다. 반환하는 오류 메시지도 서비스에서 정한 문구를 사용한다.

이런 상황을 매번 실제 모델로 재현하기는 어렵다. Client 테스트에서는 HTTP 전송을 대체해 미리 정한 응답을 반환하고, Handler 테스트에서는 모의 Client로 결과나 오류를 전달한다.

| 테스트에 넣은 상황 | 확인하는 동작 |
| --- | --- |
| 추론 항목 뒤에 여러 메시지가 오는 응답 | 텍스트를 빠짐없이 합치는가 |
| 일부 답변이 있는 `incomplete` 응답 | 성공 답변으로 내보내지 않는가 |
| 공백뿐인 질문 | Client를 호출하지 않는가 |
| 시간 초과와 요청 취소 | 오류·취소가 호출 경로를 따라 전달되는가 |

테스트 코드는 [Client 테스트](https://github.com/Coen90/llm-backend-lab/blob/feature/stage-01-llm-serving/services/agent/internal/llm/client_test.go)와 [Handler 테스트](https://github.com/Coen90/llm-backend-lab/blob/feature/stage-01-llm-serving/services/agent/internal/chat/handler_test.go)에 있다. `services/agent`에서 `go test ./...`로 실행할 수 있으며, 실제 OpenAI API를 호출하지 않는다.

## 4. 구현을 돌아보며 기술 선택에 답하기

### 익숙한 Java 대신 Go를 쓰면서 무엇을 얻고 싶은가

이번 프로젝트에서는 **여러 LLM과 도구 호출을 동시에 실행하고, 필요한 시점에 중단할 수 있는 서버를 직접 설계하는 경험**을 얻고 싶다. Agent Runtime으로 발전시키면 한 요청 안에서도 여러 작업이 함께 실행될 수 있다. 어떤 작업을 병렬로 실행할지, 얼마나 기다릴지, 하나가 실패하면 나머지도 중단할지 결정해야 한다.

Go를 고른 이유는 goroutine과 context로 이런 실행 흐름을 직접 다뤄보고 싶었기 때문이다. goroutine으로 작업을 나누고 context로 시간 제한과 취소를 전달하면서, 동시에 실행되는 작업의 시작부터 종료까지 책임지는 코드를 작성해보려 한다. 이후에는 동시 실행 수를 제한하고, 요청이 끝난 뒤에도 남아 있는 작업이 없는지 확인하는 데까지 확장할 생각이다.

Java/Spring에서 쌓은 API와 업무 로직 개발 경험에, Go로 작업 실행과 자원을 제어하는 경험을 더하고 싶다. 최종적으로는 Agent가 모델과 도구를 호출하는 동안 서버가 실행 상태를 관리하고 실패를 처리하도록 만드는 것이 목표다. 이번 API에 넣은 호출 시간 제한과 취소 전파는 그 출발점이다.

### 서버에는 Gin을 쓰면서 LLM SDK는 왜 쓰지 않았나

처음에는 서버도 표준 라이브러리로 구현했다. 그런데 서버 처리 코드가 많아지면서 배우고 싶은 LLM 호출 흐름이 잘 보이지 않았다. Gin으로 반복되는 HTTP 처리를 줄인 이유다.

LLM 호출은 요청과 응답이 오가는 방식을 직접 배우고 싶어 HTTP로 구현했다. 학습하려는 부분을 기준으로 라이브러리를 쓸 범위를 정한 셈이다. 구조를 이해한 뒤 기능이 늘어나면 SDK로 전환하는 것도 검토할 수 있다. 직접 구현한 경험은 SDK 밖의 처리가 필요할 때도 도움이 될 것 같다.

### 완성된 답변을 한 번에 보내도 괜찮을까

빠르게 끝나는 짧은 답변이라면 기다림이 크지 않을 것이라고 생각했다. 하지만 모델이 느리거나 답변이 길면 사용자는 아무 내용도 보지 못한 채 기다려야 한다.

이번에는 모델 응답이 완성되면 HTTP 응답을 한 번에 보내는 방식으로 기본 흐름을 완성했다. 여기서 동기 응답은 한 요청이 완성된 답변을 기다린다는 뜻이며, 서버 전체가 요청을 하나씩만 처리한다는 의미는 아니다. 다음 단계에서는 스트리밍을 추가하기로 했다.

### 이전 대화는 어떻게 기억하게 할까

처음에는 대화를 모두 저장하고 새 질문과 함께 보내는 방법을 떠올렸다. 대화가 길어지면 입력 토큰도 늘어나므로 내용을 요약하는 방법도 생각했다.

다만 기록을 저장하는 일과 모델에 보낼 Context를 구성하는 일은 구분해야 한다. 원본은 보관하면서 최근 대화와 이전 대화의 요약을 조합할 수 있다. 요약에도 비용이 들고 정보가 빠질 수 있으므로 대화 상태를 추가할 때 그 기준을 정하려고 한다.

### 저렴한 모델을 쓰면 전체 비용도 가장 낮을까

보유한 OpenAI API 크레딧으로 반복 실험하기 위해 `gpt-5-nano`를 선택했다. 처음에는 토큰 단가에 관심이 있었지만 답변이 부족해 여러 번 다시 질문하면 원하는 결과를 얻는 데 드는 총비용은 달라질 수 있다.

용도에 따라 모델이나 reasoning effort를 바꾸는 방법을 생각했다. 답변 평가에도 LLM을 쓸 수 있겠지만 우선 대표 질문을 정하고 사람이 읽거나 코드로 확인할 수 있는 기준부터 마련하려고 한다. 이후 품질, 응답 시간, 사용량을 함께 비교할 계획이다.

---

## 다음 글 예고: 스트리밍 응답

다음 단계에서는 스트리밍 응답을 추가해 모델이 생성 중인 내용을 사용자에게 먼저 전달한다. 긴 답변이 끝날 때까지 기다리던 사용자가 생성된 부분부터 읽을 수 있게 만드는 것이 목표다.

첫 내용이 보이기까지 걸린 시간과 답변 전체가 완료되는 시간을 나눠 살펴보려고 한다. 스트리밍 도중 호출이 실패하거나 사용자가 연결을 끊었을 때 어떻게 처리할지도 함께 다룰 예정이다.
