import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach, beforeEach } from 'vitest';

if (!Element.prototype.getAnimations) {
  Element.prototype.getAnimations = () => [];
}

beforeEach(() => {
  localStorage.clear();
});

afterEach(cleanup);
