## Context

O repositório contém apenas a fundação documental do Manu. `PRODUCT.md` posiciona o `Knowledge Engine` como núcleo, `DOMAIN.md` já define fontes, artefatos, observações, claims, evidências, proveniência e tipos de conhecimento, e `ARCHITECTURE.md` descreve analisadores, correlação, `AI Gateway` e implantação celular conceitual. Nenhum desses documentos ainda oferece um contrato explícito para comparar a profundidade de analisadores diferentes ou avaliar se uma base foi compreendida.

A mudança é transversal porque precisa manter coerentes produto, domínio, arquitetura, critérios do MVP e futuras extensões por fonte. Consulte [proposal.md](proposal.md) para a motivação e [specs/knowledge-engine-comprehension/spec.md](specs/knowledge-engine-comprehension/spec.md) para o comportamento exigido.

## Goals / Non-Goals

**Goals:**

- estabelecer uma linguagem comum para resultados parciais de analisadores especializados;
- tornar cobertura, lacunas, incerteza, suporte e contexto temporal examináveis;
- separar reconstrução estática, observação operacional e interpretação de negócio;
- definir uma forma repetível de avaliar evolução do motor;
- manter IA e mecanismos físicos atrás das fronteiras conceituais já existentes;
- distribuir cada alinhamento na fonte documental canônica apropriada.

**Non-Goals:**

- escolher linguagem, framework, banco, mecanismo de grafo, parser ou protocolo de plugins;
- definir estruturas físicas, APIs, schemas de persistência ou processos executáveis;
- selecionar agora todos os analisadores ou prometer cobertura uniforme de linguagens e plataformas;
- incluir logs, métricas, traces ou diagnóstico de causa raiz no MVP;
- fixar limiares comerciais, SLAs ou uma pontuação única de qualidade;
- implementar o Docker Compose ou um modo operacional de ingestão.

## Decisions

### 1. Contrato universal com produção especializada

Cada analisador permanece especializado na semântica de sua fonte, mas publica contribuições compatíveis com dimensões universais do conhecimento. O contrato descreve significado e suporte, não um formato de serialização.

```text
Source
  │
  ▼
analisador especializado ──► resultado + cobertura + lacunas
                                      │
                                      ▼
                         contrato universal de compreensão
                                      │
                                      ▼
                 correlação ──► base de conhecimento viva
```

As dimensões universais iniciais são:

1. paisagem, inventário e estrutura;
2. entidades e relações;
3. fluxos e dependências;
4. decisões, condições e origens dos dados usados;
5. variações por configuração, ambiente e feature flag;
6. capacidades disponíveis e como acessá-las;
7. erros, sua criação, propagação e fluxos possíveis;
8. evolução entre revisões, releases, configurações e implantações;
9. correspondência e divergência documental;
10. evidências, proveniência, incerteza e lacunas.

Uma fonte pode contribuir apenas para parte delas. As dimensões orientam correlação e avaliação, mas não obrigam todos os analisadores a produzir a mesma granularidade.

**Alternativas consideradas:**

- Contratos independentes por analisador: preservariam liberdade local, mas transfeririam a complexidade de integração para todas as experiências e impediriam comparação de cobertura.
- Um grafo genérico como único contrato: facilitaria relações, mas não representaria sozinho origem epistemológica, cobertura, temporalidade, conflitos e comportamento.
- Um analisador universal: reduziria interfaces, mas perderia semântica específica e aprofundamento progressivo por linguagem ou plataforma.

### 2. Cobertura como resultado contextual, não selo binário

Suporte a uma fonte não será expresso apenas como “suportado” ou “não suportado”. Cada execução registra escopo tentado e situação por dimensão: resultado produzido, incompleto, não suportado, não aplicável ou falha. Níveis ou métricas mais detalhados poderão evoluir depois, desde que preservem essas distinções.

**Alternativa considerada:** matriz binária de compatibilidade. Ela seria simples comercialmente, mas induziria promessa falsa de profundidade uniforme e ocultaria falhas parciais.

### 3. Qualificadores ortogonais para não fabricar certeza

O modelo conceitual será ampliado sem substituir `Observed knowledge`, `Generated knowledge` e `Curated knowledge`. Esses termos continuam respondendo pela origem. Outras perguntas usam qualificadores independentes:

| Pergunta | Qualificador conceitual |
| --- | --- |
| Como foi produzido? | observado, gerado ou curado |
| O que o sustenta? | evidências, proveniência e estado de contestação |
| Em que contexto vale? | fonte, revisão, tempo, ambiente e implantação disponíveis |
| Que realidade comportamental descreve? | caminho possível, execução observada ou processo de negócio |
| O que não sabemos? | lacuna explícita e cobertura da análise |

Não haverá um único número de confiança que substitua essas dimensões. Uma pontuação futura poderá resumi-las para uma experiência, mas deverá permitir que o usuário inspecione os fatores subjacentes.

**Alternativa considerada:** confiança escalar universal. É conveniente para ordenar resultados, porém mistura qualidade da extração, atualidade, existência de evidência, revisão humana e ocorrência real.

### 4. Três sentidos canônicos de fluxo

`Flow` continua sendo o conceito geral já presente em `DOMAIN.md`, mas seu uso será qualificado:

- `Possible Flow`: caminho que pode ocorrer segundo código, contratos e configurações analisados;
- `Observed Execution`: ocorrência sustentada por evidência operacional;
- `Business Process`: atividades e decisões orientadas a um resultado de negócio, possivelmente apoiadas por vários fluxos.

Sem logs, métricas ou traces, o MVP pode reconstruir `Possible Flow`, mas não afirmar `Observed Execution`. Documentos e curadoria podem relacionar um fluxo a um processo de negócio, preservando a origem dessa interpretação.

### 5. Perguntas de competência e referências revisáveis

O diferencial do motor será avaliado por sua capacidade de responder perguntas úteis, não por volume de nós, embeddings ou páginas. O conjunto inicial de perguntas cobrirá as dimensões universais e será aplicado ao corpus heterogêneo do MVP.

Cada caso de avaliação relacionará:

- a pergunta e o público que a utiliza;
- o recorte e a revisão das fontes analisadas;
- uma resposta de referência revisável e seus autores;
- evidências esperadas, quando existirem;
- resultados do Manu, incluindo resposta, suporte, omissões e abstinências;
- critérios de correção, cobertura, rastreabilidade, atualidade e falsa certeza.

Os limiares só serão definidos após uma linha de base. O conjunto deve ser versionado para distinguir evolução do produto de mudança no teste.

**Alternativas consideradas:** avaliar por documentação produzida ou demonstração livre. Ambas geram sinais úteis, mas dificultam regressão, comparação e identificação de respostas convincentes sem suporte.

### 6. IA posterior à evidência e com degradação funcional

Analisadores, correlação e curadoria produzem ou organizam o conhecimento sustentado. A IA pode sintetizar, explicar, classificar e intermediar linguagem natural, mas sua saída permanece `Generated knowledge` e não é evidência autossuficiente.

```text
análise autorizada ──► observações/evidências ──► correlação/claims
                                                        │
                                  ┌─────────────────────┴──────────┐
                                  ▼                                ▼
                         experiências básicas              IA autorizada
                         continuam disponíveis       síntese e explicação
```

Quando o modelo estiver ausente ou seu uso for proibido, apenas as etapas dependentes dele ficam indisponíveis. Acesso à fonte e transferência externa continuam decisões de política separadas. Isso preserva utilidade em ambientes on-premises restritivos mesmo enquanto modelos locais permanecem opção futura.

**Alternativa considerada:** pipeline centrado em LLM. Aceleraria protótipos de explicação, mas tornaria evidência, custo, previsibilidade, privacidade e funcionamento sem provedor dependentes do modelo.

### 7. Contextos temporais relacionados, não condensados em “versão”

O domínio distinguirá `Source Revision`, `Analysis Snapshot`, `Environment`, `Release`, `Build Artifact`, `Deployment`, `Configuration State` e `Documentation Revision`. Nem todos estarão disponíveis em toda análise; os vínculos conhecidos e ausentes devem ser explícitos.

Uma comparação primeiro identifica os contextos disponíveis e só então explica diferenças. Isso permite dizer “configurações diferem” sem afirmar “o código implantado difere” quando não houver ligação entre a revisão analisada e o artefato implantado.

**Alternativa considerada:** um campo genérico de versão. Seria menor, mas perderia diferenças fundamentais entre fonte, build, implantação, configuração e documentação.

### 8. `Capability` e `Knowledge Product` têm identidades distintas

`Capability` representa algo que o ambiente analisado oferece ou permite realizar. `Knowledge Product` representa uma composição consumível produzida pelo Manu, como uma página, mapa, explicação ou relatório de impacto. Um produto de conhecimento pode documentar ou relacionar capacidades, mas não se torna parte do sistema analisado.

Essa distinção resolve a ambiguidade de “relatório”: um relatório existente no cliente é uma capacidade ou recurso descoberto; um relatório de impacto criado pelo Manu é um produto de conhecimento.

### 9. Registro da decisão arquitetural

O contrato universal entre analisadores especializados e a base satisfaz os critérios para um ADR: foi aceito, orientará extensões futuras, tem custo alto de reversão e resulta de trade-offs reais contra contratos isolados, grafo único e análise centrada em IA. A aplicação desta mudança criará o primeiro ADR do projeto para preservar esse contexto; os documentos canônicos continuarão contendo as regras de leitura normal.

## Risks / Trade-offs

- [Contrato universal excessivamente abstrato] → validar cada dimensão com perguntas e exemplos reais do corpus do MVP; não criar conceitos sem uso demonstrável.
- [Menor profundidade comum limitar analisadores avançados] → permitir extensões especializadas preservando uma projeção para o contrato universal.
- [Cobertura detalhada confundir usuários não técnicos] → manter os detalhes inspecionáveis e permitir resumos por experiência sem apagar lacunas.
- [Referências de avaliação refletirem opinião de poucos especialistas] → versionar autoria, evidências e revisões, tratando a referência como curável e contestável.
- [Métricas incentivarem otimização para o conjunto conhecido] → combinar regressão repetível com aplicações e perguntas novas durante a validação.
- [Funcionamento sem IA ser interpretado como equivalência completa] → indicar claramente quais experiências estão disponíveis, limitadas ou indisponíveis.
- [Explosão de contextos temporais] → registrar somente contextos sustentados e permitir desconhecidos explícitos, sem exigir todos em cada análise.

## Migration Plan

Esta é uma evolução documental, sem implantação ou migração de dados:

1. atualizar `PRODUCT.md` com o contrato de compreensão, perguntas de competência e critério de validação do MVP;
2. atualizar `DOMAIN.md` com termos e distinções canônicas, removendo questões abertas que forem resolvidas por esta mudança;
3. atualizar `ARCHITECTURE.md` com o contrato entre analisadores e engine, cobertura, fronteira da IA, acesso às fontes e contextos temporais;
4. criar o ADR aceito do contrato universal, referenciando esta mudança e os documentos canônicos;
5. revisar navegação, linguagem, estados de decisão e ausência de promessas de implementação;
6. validar a mudança OpenSpec e os links relativos.

Como não há código nem dados executáveis, rollback consiste em reverter conjuntamente as alterações documentais antes do arquivamento. Depois de arquivada, uma revisão incompatível deverá ser proposta por nova mudança e, se substituir o ADR, marcar a decisão anterior como `Superseded`.

## Open Questions

- Quais duas a quatro aplicações formarão o primeiro corpus heterogêneo de validação?
- Qual banco inicial de perguntas por público oferece cobertura suficiente sem tornar o MVP horizontal demais?
- Quais limiares de correção, cobertura e abstinência serão adotados após a primeira linha de base?
- Qual representação resumida de cobertura será mais compreensível para cada experiência, preservando os detalhes examináveis?
