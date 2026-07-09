FROM factoriotools/factorio:2.1.9

ENV POETRY_VIRTUALENVS_CREATE=0
WORKDIR /factorio
COPY ./config config
COPY ./scripts scripts
COPY ./mods mods
COPY pyproject.toml poetry.lock ./
COPY .fly/secrets.dev ./
RUN apt update && \
    apt install --yes python3 jq
RUN curl -sSL https://install.python-poetry.org | python3 - && \
    export PATH="/root/.local/bin:$PATH" && \
    poetry install --no-interaction --no-root

RUN echo "alias download='/root/.local/bin/poetry run python3 /factorio/scripts/save_file_manager.py --download'" >> /root/.bashrc
RUN echo "alias upload='/root/.local/bin/poetry run python3 /factorio/scripts/save_file_manager.py --upload'" >> /root/.bashrc

RUN export $(grep -v '^\s*#' secrets.dev | grep -v '^\s*$' | xargs) && \
    /root/.local/bin/poetry run python3 /factorio/scripts/save_file_manager.py --download

# Copy and set up entrypoint script
COPY ./scripts/entrypoint.sh /factorio/entrypoint.sh
RUN chmod +x /factorio/entrypoint.sh

# Override the default entrypoint to use our wrapper script
ENTRYPOINT ["/factorio/entrypoint.sh"]
