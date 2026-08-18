## Why

O Manu já define o `Knowledge Engine` como núcleo, mas ainda não estabelece o que significa compreender uma base heterogênea nem como demonstrar essa compreensão sem confundir possibilidade, execução observada, inferência e explicação por IA. Esse contrato é necessário antes de escolher stack ou mecanismos físicos, pois delimita o diferencial do produto e os critérios pelos quais seu motor será avaliado.

## What Changes

- Define um contrato universal de compreensão que analisadores especializados devem alimentar progressivamente, sem prometer a mesma profundidade para todas as fontes.
- Organiza a compreensão em dimensões comuns: inventário e estrutura, relações, fluxos, decisões, variações configuráveis, capacidades, erros, evolução, documentação, evidências e lacunas.
- Exige que cada resultado declare cobertura e suporte disponível, preservando a diferença entre caminho possível, execução observada e processo de negócio.
- Estabelece perguntas de competência e critérios verificáveis para avaliar se o `Knowledge Engine` compreendeu uma base, em vez de usar volume de documentação gerada como medida de sucesso.
- Delimita a IA como recurso de explicação, síntese e consulta, nunca como evidência técnica autossuficiente nem como condição para o núcleo continuar útil.
- Introduz o contexto temporal necessário para comparar versões, configurações, ambientes, implantações e revisões documentais sem antecipar um modelo físico.
- Diferencia capacidades encontradas no ambiente analisado de produtos de conhecimento produzidos pelo Manu.
- Mantém seleção de stack, protocolo de analisadores, persistência física, ingestão operacional e implementação fora desta mudança.

## Capabilities

### New Capabilities

- `knowledge-engine-comprehension`: Define o contrato universal de compreensão, a qualificação dos resultados, as perguntas de competência, a cobertura progressiva dos analisadores e os limites da assistência por IA.

### Modified Capabilities

Nenhuma.

## Impact

- Atualiza a definição do produto em `PRODUCT.md`, incluindo o critério de compreensão e sua validação no MVP.
- Amplia o vocabulário conceitual em `DOMAIN.md` para representar dimensões de compreensão, cobertura, fluxos, capacidades, produtos de conhecimento e contextos temporais.
- Refina `ARCHITECTURE.md` com responsabilidades universais e especializadas do `Knowledge Engine`, fronteira da IA e princípios de acesso às fontes.
- Não altera código, APIs, dependências, stack ou implantação executável; o repositório continua em fundação documental.
