# AGENTS.md

## Escopo

O Manu ainda está em descoberta, mas este repositório já contém um primeiro
corte executável em Go: a CLI como modo do `Manu Agent`, uma API HTTP local e
uma imagem Docker Linux multi-stage. O Agent continua local, determinístico e
limitado; a plataforma local acrescenta `manu migrate`, `manu serve`, clientes
de ingestão/consulta, PostgreSQL como fonte de verdade e projeções
reconstruíveis. O corte não constitui o produto completo: não inclui
autenticação, daemon remoto, IA local, UI, SaaS compartilhado ou operação de
produção. A integração operacional Agent → bundle estendido → ingestão no
processo servidor foi verificada na célula local Linux; o registro está em
[`docs/verification/10-3-local-cell.md`](docs/verification/10-3-local-cell.md).
O staging durável e a recuperação após reinício estão cobertos nessa
verificação. Isso não amplia o corte para autenticação, operação remota ou
produção.

Os comandos canônicos para construir, executar e verificar os limites atuais
estão em [`README.md`](README.md), [`docs/cli-http.md`](docs/cli-http.md),
[`docs/compose.md`](docs/compose.md) e
[`docs/configuration.md`](docs/configuration.md). Não invente capacidades,
comandos, diretórios, ferramentas ou decisões para preencher lacunas além do
que estiver especificado nas mudanças OpenSpec em escopo; se um comando ou
integração ainda não existir, documente a ausência.

## Ordem de leitura

1. [`README.md`](README.md) para a entrada e o mapa do projeto.
2. [`PRODUCT.md`](PRODUCT.md) para problema, públicos, valor e MVP.
3. [`DOMAIN.md`](DOMAIN.md) para a linguagem e o modelo conceitual.
4. [`ARCHITECTURE.md`](ARCHITECTURE.md) para arquitetura, fluxos, restrições e implantação.
5. [`docs/decisions/README.md`](docs/decisions/README.md) quando a tarefa envolver uma decisão arquitetural.
6. A mudança OpenSpec relevante em [`openspec/`](openspec/), lendo `proposal.md`, `design.md` e `tasks.md` antes de trabalhar.

## Regras operacionais

- Faça somente o que estiver planejado e especificado na mudança OpenSpec em escopo; preserve seus critérios de aceitação.
- Use cada documento para sua responsabilidade própria: produto em `PRODUCT.md`, arquitetura em `ARCHITECTURE.md`, domínio em `DOMAIN.md`, operação neste arquivo e decisões aceitas em `docs/decisions/`.
- Prefira links para fontes canônicas a copiar conteúdo. Quando uma mudança afetar mais de uma fonte, atualize os documentos afetados juntos e preserve a navegação.
- Escreva a documentação em português brasileiro. Use os nomes canônicos do domínio definidos em `DOMAIN.md`, com definições em português.
- Separe explicitamente restrições atuais, decisões aceitas, hipóteses e opções futuras. Separe também conhecimento observado, gerado e curado; não transforme hipótese em compromisso nem sobrescreva conteúdo curado silenciosamente.
- Registre uma decisão aceita, difícil de reverter e baseada em trade-offs conforme a política e o template de [`docs/decisions/README.md`](docs/decisions/README.md).
- Antes de concluir uma mudança documental, verifique links relativos, ausência de placeholders, responsabilidade única dos documentos, termos coerentes e alinhamento entre Knowledge Engine, base de conhecimento viva e experiências derivadas.

## Comunicação e eficiência de contexto

- Quando a skill `caveman` estiver disponível, use o nível `full` nas
  respostas ao usuário, atualizações de progresso e retornos entre agentes.
  Preserve precisão técnica, negações, números, unidades, comandos, símbolos
  de código e mensagens de erro exatas.
- A compressão vale somente para a comunicação da sessão. Código, comentários,
  documentação, artefatos OpenSpec, mensagens de commit e textos destinados a
  outras pessoas continuam em linguagem normal e seguem as regras editoriais
  deste repositório.
- Prefira retornos pequenos e verificáveis: resultado, evidência decisiva,
  risco e próximo passo. Não despeje logs completos quando a menor linha que
  prova sucesso ou falha for suficiente. Não esconda falhas, limitações ou
  ordem necessária de operações para economizar tokens.
- Trate economia de tokens como hipótese mensurável. Diferencie estimativa,
  contagem reportada pelo provedor e comparação controlada; não publique
  percentuais de economia ou equivalência de qualidade sem dados reproduzíveis.
- O uso da skill de estilo não autoriza instalar ou incorporar o `Caveman
  Proxy`, `Engine`, MCP, memória, hooks ou outros runtimes. Qualquer adoção
  dessas superfícies exige mudança OpenSpec própria, revisão de licença,
  segurança, privacidade, telemetria, recuperação byte a byte e compatibilidade
  com o corte local do Manu. Não copie código coberto por BSL-1.1 para o núcleo.
- Fontes externas para essa política: [repositório Caveman](https://github.com/JuliusBrussee/caveman),
  [limites das medições](https://github.com/JuliusBrussee/caveman/blob/main/docs/HONEST-NUMBERS.md),
  [segurança e privacidade](https://github.com/JuliusBrussee/caveman/blob/main/SECURITY.md) e
  [escopo de licenças](https://github.com/JuliusBrussee/caveman/blob/main/LICENSING.md).

## OpenSpec e verificação

Leia os artefatos da mudança antes de editar e mantenha a implementação
limitada às tarefas previstas. Para uma mudança ativa, as verificações atuais
incluem formatação, análise estática, testes, builds estáticos, integridade do
módulo e, quando a célula local for afetada, validação estrutural do Compose:

```text
gofmt -d <arquivos Go>
go vet ./...
go test ./... -count=1
go mod verify
go test ./docs
git diff --check
docker compose config --quiet
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o <saída> ./cmd/manu
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o <saída> ./cmd/manu
openspec validate <change> --strict
```

Substitua `<change>` pelo identificador da mudança OpenSpec ativa; não fixe a
verificação em uma mudança arquivada. Os comandos completos, a verificação da
imagem Docker e as limitações de ferramentas opcionais ficam no
[`README.md`](README.md). Execute `go test -race ./...` somente quando CGO e um
compilador C estiverem disponíveis; se `govulncheck` ou outra ferramenta
opcional não estiver disponível, registre a ausência sem alegar que a
verificação foi feita.

## Development workflow

The primary agent is the planner and orchestrator.

### Planning

Planning, architecture decisions, requirements analysis,
OpenSpec proposals, design and task decomposition are handled
by the primary agent.

The primary agent must not implement application code.

### Implementation

Once an OpenSpec change is sufficiently specified:

1. Break the implementation into small independent tasks.
2. Delegate implementation to `implementer` subagents.
3. Wait for the implementers to finish.
4. Review their changes against the OpenSpec.
5. If corrections are required, delegate them back to an
   `implementer`.
6. Never implement or fix the code directly from the primary agent.

### Model policy

- Planning/orchestration: GPT-5.6 Sol
- Implementation: GPT-5.6 Luna
- Sol must never be used for implementation.
