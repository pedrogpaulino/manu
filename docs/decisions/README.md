# Architecture Decision Records

Este diretório guarda decisões arquiteturais aceitas cujo contexto e
trade-offs precisam sobreviver à conversa que as originou. A arquitetura geral
está em [`ARCHITECTURE.md`](../../ARCHITECTURE.md); este arquivo define quando
uma escolha merece um ADR e como mantê-lo pequeno.

## Quando criar um ADR

Crie um ADR individual somente quando todos os critérios abaixo forem
verdadeiros:

1. a escolha foi **aceita**, e não apenas proposta, hipótese ou opção futura;
2. ela é **difícil de reverter** ou teria custo/rastro relevante se fosse
   desfeita;
3. houve um **trade-off real** entre alternativas plausíveis;
4. registrar o contexto ajudará decisões futuras a entenderem por que a
   alternativa escolhida prevaleceu.

Uma decisão que ainda precisa de experimento deve permanecer no documento
canônico como `Hypothesis`. Uma direção possível deve ser marcada como `Future
option`. Não use ADR para registrar preferências triviais, tarefas, detalhes
de implementação temporários ou uma lista de ideias.

## ADRs publicados

- [`0001-contrato-universal-de-compreensao.md`](0001-contrato-universal-de-compreensao.md)
  — decisão `Accepted` sobre analisadores especializados projetados em um
  contrato universal de compreensão.
- [`0002-fundacao-go-first.md`](0002-fundacao-go-first.md)
  — decisão `Accepted` sobre o runtime Go-first, o módulo único e a política
  de atualização do toolchain.

## Estados

O estado aparece no front matter e no título do ADR. Use exatamente um dos
seguintes valores:

- `Proposed` — contexto e alternativas estão em discussão; não é uma regra
  vigente.
- `Accepted` — a escolha foi aprovada e deve orientar o trabalho aplicável.
- `Superseded` — uma decisão aceita foi substituída por outro ADR; aponte para
  o ADR substituto e preserve o histórico.
- `Rejected` — a proposta foi considerada e descartada; registre a razão para
  não reabrir a mesma discussão sem contexto novo.

Somente `Accepted` define uma regra arquitetural vigente. Um ADR não deve ser
apagado para esconder uma decisão anterior: atualize seu estado e estabeleça o
vínculo com a decisão que o substituiu, quando houver.

## Convenção de nomes e localização

Arquivos usam o padrão `NNNN-titulo-kebab-case.md`, com quatro dígitos
sequenciais e título curto em kebab-case, por exemplo
`0001-limite-de-uma-organizacao-por-instalacao.md`. O número é permanente,
mesmo se o título precisar ser esclarecido; não reutilize números.

O idioma dos ADRs é português brasileiro. Termos canônicos de domínio, como
`Organization`, `Source`, `Knowledge Engine` e `AI Gateway`, permanecem no
nome em inglês quando essa é a forma usada pelo projeto, com explicação em
português. Um ADR deve referenciar documentos canônicos e mudanças OpenSpec
por links relativos, sem copiar seu conteúdo.

## Processo

1. Durante a exploração, registre a questão em OpenSpec ou no documento
   canônico como hipótese, restrição ou opção futura.
2. Compare alternativas e explicite o trade-off; não promova uma direção a
   decisão apenas por estar desenhada em um diagrama.
3. Quando a escolha for aceita e cumprir os critérios acima, crie o arquivo a
   partir do template e atribua o estado `Accepted`.
4. Atualize `ARCHITECTURE.md`, `DOMAIN.md` ou `PRODUCT.md` quando a decisão
   afetar sua fonte de verdade. O ADR guarda o porquê; o documento canônico
   guarda a regra que leitores precisam encontrar no fluxo normal.
5. Se uma mudança OpenSpec alterar ou substituir a decisão, atualize o ADR e
   os documentos afetados na mesma mudança, sem editar o histórico para fazê-lo
   parecer uma decisão diferente.

## Template mínimo

Copie a estrutura abaixo para um novo arquivo numerado e remova as instruções
entre colchetes antes de aceitar o ADR.

```markdown
---
status: Proposed
date: YYYY-MM-DD
---

# ADR NNNN: [título curto]

## Contexto

[Qual problema ou decisão exige registro? Inclua restrições relevantes.]

## Decisão

[O que foi escolhido? Use linguagem normativa somente quando o estado for
Accepted.]

## Alternativas consideradas

- [Alternativa A]: [benefícios, custos e motivo para não escolher]
- [Alternativa B]: [benefícios, custos e motivo para não escolher]

## Trade-offs e consequências

- [Consequência positiva ou risco aceito]
- [O que fica mais difícil ou limitado]

## Relações

- OpenSpec: [link relativo, se houver]
- Documentos afetados: [links relativos]
- ADR substituído/substituto: [link, se houver]
```

Um ADR deve ser autocontido o suficiente para explicar a escolha, mas não deve
duplicar o glossário, requisitos do produto ou tarefas. Se a decisão ainda não
puder ser escrita com contexto, alternativas e consequências verificáveis,
retorne-a ao estado de hipótese ou proposta.
