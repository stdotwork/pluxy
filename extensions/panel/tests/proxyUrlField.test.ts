import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nextProvider } from 'react-i18next';
import i18n from '@/i18n';
import { ProxyUrlField } from '@/features/config/components/fields/sharedFields';
import { DEFAULT_VISUAL_VALUES } from '@/types/visualConfig';

const translations = i18n.cloneInstance({ lng: 'en' });

describe('proxy URL field', () => {
  test('shows the label and the value in an input, with no link or image next to it', () => {
    const markup = renderToStaticMarkup(
      createElement(
        I18nextProvider,
        { i18n: translations },
        createElement(ProxyUrlField, {
          values: { ...DEFAULT_VISUAL_VALUES, proxyUrl: 'socks5://127.0.0.1:1080' },
          disabled: false,
          onChange: () => {},
        })
      )
    );

    expect(markup).toContain(translations.t('config_management.visual.sections.network.proxy_url'));
    expect(markup).toContain('value="socks5://127.0.0.1:1080"');
    expect(markup).not.toContain('<a ');
    expect(markup).not.toContain('<img');
  });
});
