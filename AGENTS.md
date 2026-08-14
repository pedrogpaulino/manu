# AGENTS.md

## Escopo

O Manu está em descoberta e este repositório mantém sua fundação documental. Não há aplicação, stack, dependências, comandos de build, implantação ou suíte de testes estabelecidos neste estágio. Não invente comandos, diretórios, ferramentas ou decisões para preencher lacunas.

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

## OpenSpec e verificação

Leia os artefatos da mudança antes de editar e mantenha a implementação limitada às tarefas previstas. A verificação disponível para a mudança atual é:

```text
openspec validate establish-project-documentation-foundation
```

Não há testes de aplicação a executar enquanto não existir código ou uma suíte definida.

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
