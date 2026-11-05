import { describe, expect, mock, test } from 'bun:test';
import { join } from 'node:path';
import { isValidElement } from 'react';
import { Navigate, matchRoutes } from 'react-router-dom';

// Importing the route table loads every page. Bun resolves a CSS Module to a path string, so the
// two modules that must export the quota class contract are replaced by echoing class names.
const echoClasses = () => ({
  default: new Proxy({}, { get: (_, key) => (typeof key === 'string' ? key : undefined) }),
});
mock.module(
  join(import.meta.dir, '../src/features/authFiles/components/AuthFileQuota.module.scss'),
  echoClasses
);
mock.module(
  join(import.meta.dir, '../src/features/quota/components/QuotaBody.module.scss'),
  echoClasses
);

const { createMainRoutes } = await import('../src/router/MainRoutes');

const redirectTarget = (path: string, supportsPlugin = true): string | null => {
  const matches = matchRoutes(createMainRoutes(supportsPlugin), path);
  const element = matches?.[matches.length - 1]?.route.element;
  if (!isValidElement<{ to: string }>(element) || element.type !== Navigate) return null;
  return element.props.to;
};

describe('main route table', () => {
  test('old Quick start bookmarks land on the providers page', () => {
    expect(redirectTarget('/quick-start')).toBe('/ai-providers');
    expect(redirectTarget('/quick-start/anything')).toBe('/ai-providers');
  });

  test('the providers page itself is not a redirect', () => {
    expect(redirectTarget('/ai-providers')).toBeNull();
  });
});
