---
status: Accepted
date: 2026-08-18
---

# ADR 0004: AI Gateway independente de provedor e capacidades separadas

## Contexto

O fluxo de consulta pode precisar de embeddings para recuperação e de geração
para produzir uma resposta citada, mas essas capacidades têm ciclos de vida,
perfis, custos, limites e falhas diferentes. O núcleo do `Knowledge Engine`
precisa continuar independente do fornecedor, inclusive quando uma chamada
externa for proibida ou estiver indisponível. Credenciais devem permanecer
fora dos bundles, documentos, logs e contratos do domínio.

Uma API que se declara compatível com OpenAI pode divergir em protocolo,
parâmetros, capacidades, erros e modelo efetivo. Tratar essa compatibilidade
como o próprio contrato do domínio esconderia essas diferenças e tornaria a
troca de provedor uma alteração transversal.

## Decisão

Adotamos um `AI Gateway` como fronteira explícita entre o núcleo e os modelos,
com duas portas independentes:

- `Embedder`, para gerar embeddings de unidades autorizadas em lotes; e
- `Generator`, para receber uma pergunta e um pacote limitado de evidências e
  produzir uma resposta estruturada.

Requests, resultados de uso, latência, término, modelo efetivo e erros
normalizados atravessam essas portas em DTOs internos. Tipos e respostas
específicos de um provedor permanecem dentro do adaptador correspondente. As
configurações, credenciais, deadlines, lotes e orçamentos de embedding e
geração são independentes; trocar o gerador não exige reindexar embeddings,
enquanto trocar o perfil de embedding exige o rebuild definido no [ADR 0003](0003-postgresql-fonte-de-verdade-pgvector-projecao.md).

Os adaptadores são explícitos: o adaptador nativo da API OpenAI e o adaptador
de um protocolo `OpenAI-compatible` configurado e validado inicialmente com
OpenRouter são integrações distintas. `OpenAI-compatible` não é um contrato
de domínio, não implica equivalência silenciosa entre provedores e não autoriza
fallback automático de protocolo ou capacidade. O gateway deve validar as
capacidades declaradas antes da chamada e registrar diferenças relevantes.

O pacote de evidências autorizado é a única entrada organizacional do
`Generator`; nenhum adaptador recebe acesso direto à `Source`, ao banco,
credenciais ou ferramentas. Chamadas reais dependem de política e
configuração explícitas; provedores simulados determinísticos sustentam a
avaliação padrão. A decisão sobre a fronteira do gateway não significa que
servidor, autenticação, produção, IA local ou qualquer adaptador já estejam
implementados no estado atual do repositório.

## Alternativas consideradas

- **Usar tipos do fornecedor no núcleo:** reduziria o trabalho do primeiro
  adaptador, mas espalharia o contrato externo por políticas, testes e fluxo de
  conhecimento.
- **Uma porta única para toda operação de IA:** simplificaria a superfície,
  mas misturaria o ciclo de vida de embeddings e geração e impediria escolher
  provedores independentes.
- **Tratar todo endpoint `OpenAI-compatible` como equivalente:** facilitaria a
  configuração inicial, mas ocultaria diferenças de protocolo, capacidade,
  parâmetros, erros e modelo efetivo.

## Trade-offs e consequências

- A separação permite substituir o gerador sem reindexação e torna explícita a
  obrigação de rebuild quando o perfil de embedding muda.
- Adaptadores isolados tornam diferenças e falhas auditáveis, ao custo de
  manter testes e mapeamentos próprios para cada protocolo suportado.
- O domínio permanece portátil e pode operar sem IA quando a política,
  evidência ou disponibilidade impedir uma chamada; respostas geradas seguem
  sendo `Generated knowledge`, nunca evidência técnica ou conhecimento
  curado por padrão.
- A escolha não fixa provedor, modelo ou execução local para uma implantação
  futura; cada integração adicional exige capacidade declarada, política e
  mudança especificada.

## Relações

- OpenSpec: [design da mudança](../../openspec/changes/define-first-evidence-backed-query-slice/design.md), [especificação da capacidade](../../openspec/changes/define-first-evidence-backed-query-slice/specs/evidence-backed-query/spec.md)
- Documentos afetados: [`ARCHITECTURE.md`](../../ARCHITECTURE.md), [índice de ADRs](README.md)
- ADR substituído/substituto: não aplicável
