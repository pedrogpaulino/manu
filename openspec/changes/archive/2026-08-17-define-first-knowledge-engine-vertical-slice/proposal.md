## Why

O contrato universal de compreensão estabelece o significado do conhecimento, mas ainda não delimita um experimento executável que demonstre extração, recuperação e resposta sustentada sobre bases reais. Definir agora o primeiro corte vertical permite validar utilidade, precisão, custo e consumo de recursos antes de escolher uma stack definitiva ou ampliar a profundidade dos analisadores.

## What Changes

- Define um corpus inicial heterogêneo com papéis distintos: Ticketmaster como referência primária de fluxo Java/Quarkus, uma amostra representativa de pacotes CAR como prova declarativa e de middleware WSO2, e ERPNext como prova de inventário e escala.
- Delimita um único `Knowledge Engine` extensível por analisadores especializados, com inventário genérico para todo o corpus, primeira profundidade semântica em Java/Quarkus e cobertura mínima explícita para WSO2 e ERPNext.
- Define o fluxo verificável de ponta a ponta entre descoberta, observações, evidências, projeções relacionais, textuais e vetoriais, recuperação híbrida e resposta gerada com citações e abstinência.
- Delimita uma experiência inicial por CLI para registrar e analisar fontes, consultar estado e cobertura, fazer perguntas, inspecionar evidências e executar avaliações e benchmarks.
- Define a OpenAI API atrás do `AI Gateway` como provedor externo inicial autorizado para embeddings e geração de respostas, mantendo interfaces substituíveis, políticas de transferência e medição de tokens, custo e latência.
- Estabelece uma estratégia de verificação em camadas para analisadores, corpus, recuperação, respostas, incrementalidade, segurança, desempenho e custo, com a maioria dos testes independente de chamadas reais à IA.
- Mantém fora deste recorte a compreensão profunda de todas as linguagens e frameworks, interface gráfica, curadoria completa, SaaS compartilhado, `Control Plane`, observabilidade operacional e escolha definitiva da stack de produção.

## Capabilities

### New Capabilities

Nenhuma.

### Modified Capabilities

- `knowledge-engine-comprehension`: delimita como o primeiro corte vertical deve demonstrar compreensão progressiva e verificável sobre o corpus inicial, incluindo cobertura por analisador, recuperação híbrida, consulta assistida por IA e avaliação reproduzível.

## Impact

- Afeta a especificação de compreensão do `Knowledge Engine` e, durante a aplicação documental da mudança, as descrições correspondentes de produto e arquitetura.
- Introduz requisitos futuros para conectores locais, analisadores, persistência PostgreSQL/pgvector, CLI, `AI Gateway`, adaptadores OpenAI e infraestrutura de avaliação, sem implementar esses componentes nesta mudança de definição.
- Cria base para uma mudança posterior de implementação e para uma decisão explícita de stack sustentada por benchmark do corte, em vez de antecipar linguagem, bibliotecas ou formato físico do domínio.
