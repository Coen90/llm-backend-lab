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

## 기술 선택과 고민

| 선택 | 이유와 고려사항 |
|---|---|
| Go | 동시 실행과 취소를 다루는 Agent Runtime 개발을 배우기 위해 선택했습니다. Java 대비 성능 우위는 가정하지 않고 이후 측정합니다. |
| Gin | 서버의 반복 코드를 줄여 LLM 통신 학습에 집중합니다. 프레임워크 의존성을 추가하는 비용을 받아들였습니다. |
| 직접 HTTP 호출 | SDK 대신 요청·응답 규격과 오류 처리를 직접 익힙니다. 기능이 늘어 유지보수 부담이 커지면 SDK 전환을 검토합니다. |
| 저렴한 모델 | 반복 실험 비용을 낮춥니다. 향후 대표 질문으로 품질·응답 시간·사용량을 비교해 모델 변경을 판단합니다. |
| Handler / Client 분리 | HTTP 처리와 모델 통신의 변경 범위를 나누고, 모의 Client로 테스트하기 위해 분리했습니다. |
| 모노레포 / 서비스별 모듈 | 개발 이력은 함께 관리하고 의존성은 서비스별로 관리합니다. 공통 모듈은 실제 중복과 필요가 생길 때 검토합니다. |

다음 스테이지에서는 스트리밍으로 첫 응답을 보여주는 시간을 개선합니다.
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

검증은 `services/agent`에서 `go test ./...`로 실행합니다. 테스트는 실제 API를 호출하지 않습니다.

참고: [OpenAI 텍스트 생성](https://developers.openai.com/api/docs/guides/text) · [Gin 시작 가이드](https://gin-gonic.com/en/docs/quickstart/)
