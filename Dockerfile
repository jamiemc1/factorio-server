FROM factoriotools/factorio:stable

ENV POETRY_VIRTUALENVS_CREATE=0
WORKDIR /factorio
COPY ./config config
COPY ./scripts scripts
COPY pyproject.toml poetry.lock ./
COPY .fly/secrets.dev ./
RUN apt update && \
    apt install --yes python3
RUN curl -sSL https://install.python-poetry.org | python3 - && \
    export PATH="/root/.local/bin:$PATH" && \
    poetry install --no-interaction --no-root

RUN echo "alias download='/root/.local/bin/poetry run python3 /factorio/scripts/save_file_manager.py --download'" >> /root/.bashrc
RUN echo "alias upload='/root/.local/bin/poetry run python3 /factorio/scripts/save_file_manager.py --upload'" >> /root/.bashrc

RUN export $(cat secrets.dev | xargs) && \
    /root/.local/bin/poetry run python3 /factorio/scripts/save_file_manager.py --download

    

